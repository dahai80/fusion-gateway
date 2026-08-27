package middleware

import (
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type keyLookupStore interface {
    GetKeyByHash(hash string) (*store.APIKeyEntry, error)
}

func APIKeyAuth(cfg *config.AuthConfig) func(http.Handler) http.Handler {
    return APIKeyAuthWithStore(cfg, nil)
}

func APIKeyAuthWithStore(cfg *config.AuthConfig, st keyLookupStore) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx, p := EnsurePrincipal(r.Context())

            if !cfg.Enabled || cfg.Passthrough {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            key := extractAPIKey(r)
            if key == "" {
                slog.Warn("missing api key", "path", r.URL.Path, "remote", r.RemoteAddr)
                http.Error(w, `{"error":{"message":"Missing API key","type":"auth_error"}}`, http.StatusUnauthorized)
                return
            }

            if cfg.MasterKey != "" && subtle.ConstantTimeCompare([]byte(cfg.MasterKey), []byte(key)) == 1 {
                slog.Info("master key authenticated", "path", r.URL.Path)
                p.AuthMethod = "apikey"
                p.IsMaster = true
                p.KeyConfig = &config.AuthKeyConfig{
                    Key:  cfg.MasterKey,
                    Name: "master",
                }
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

            var matchedKey *config.AuthKeyConfig
            for i := range cfg.APIKeys {
                if subtle.ConstantTimeCompare([]byte(cfg.APIKeys[i].Key), []byte(key)) == 1 {
                    matchedKey = &cfg.APIKeys[i]
                    break
                }
            }

            if matchedKey == nil && st != nil {
                if kc, ok := lookupKeyByHash(st, key); ok {
                    matchedKey = kc
                }
            }

            if matchedKey == nil {
                slog.Warn("invalid api key", "path", r.URL.Path, "remote", r.RemoteAddr)
                http.Error(w, `{"error":{"message":"Invalid API key","type":"auth_error"}}`, http.StatusUnauthorized)
                return
            }

            if matchedKey.ExpiresAt != "" {
                expiresAt, err := time.Parse(time.RFC3339, matchedKey.ExpiresAt)
                if err != nil {
                    slog.Error("invalid expires_at format for key", "key", matchedKey.Name, "expires_at", matchedKey.ExpiresAt)
                } else if time.Now().After(expiresAt) {
                    slog.Warn("expired api key", "key", matchedKey.Name, "expires_at", matchedKey.ExpiresAt)
                    http.Error(w, `{"error":{"message":"API key expired","type":"auth_error"}}`, http.StatusUnauthorized)
                    return
                }
            }

            p.AuthMethod = "apikey"
            p.IsMaster = false
            p.KeyConfig = matchedKey
            p.ModelModules = matchedKey.ModelModules
            slog.Debug("api key authenticated", "key", matchedKey.Name, "modules", matchedKey.ModelModules)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func lookupKeyByHash(st keyLookupStore, key string) (*config.AuthKeyConfig, bool) {
    sum := sha256.Sum256([]byte(key))
    hash := hex.EncodeToString(sum[:])
    entry, err := st.GetKeyByHash(hash)
    // AH5 (audit P0): distinguish store-error from not-found. Previously both
    // branches collapsed into `return nil, false` — fail-closed (401) but
    // silent: a store outage looked identical to a wrong key, and a future
    // "tolerant" refactor (err!=nil → allow) would silently flip to fail-open.
    // Store errors stay fail-closed (no key resolved → 401) but are logged at
    // ERROR so an outage is observable and distinguishable from bad-credential
    // noise. Not-found (entry==nil, err==nil) stays a quiet 401.
    if err != nil {
        slog.Error("key store lookup failed (fail-closed: no key resolved)",
            "hash_prefix", hash[:8], "error", err)
        return nil, false
    }
    if entry == nil {
        return nil, false
    }
    if entry.Status != "" && entry.Status != "active" {
        slog.Warn("api key disabled", "key", entry.Name, "status", entry.Status)
        return nil, false
    }
    kc := &config.AuthKeyConfig{
        Key:             key,
        Name:            entry.Name,
        AllowedBackends: entry.AllowedBackends,
        AllowedModels:   entry.AllowedModels,
        ModelModules:    entry.ModelModules,
        RPM:             entry.RPM,
        TPM:             entry.TPM,
        BudgetLimit:     entry.BudgetLimit,
        DailyBudgetLimit: entry.DailyBudgetLimit,
    }
    if entry.ExpiresAt != nil && !entry.ExpiresAt.IsZero() {
        kc.ExpiresAt = entry.ExpiresAt.Format(time.RFC3339)
    }
    return kc, true
}

func CheckModelAllowlist(r *http.Request, model string) bool {
    p := PrincipalFromContext(r.Context())
    if p != nil && p.IsMaster {
        return true
    }

    if p == nil || p.KeyConfig == nil {
        return true
    }

    if len(p.KeyConfig.AllowedModels) == 0 {
        return true
    }

    modelLower := strings.ToLower(model)
    for _, allowed := range p.KeyConfig.AllowedModels {
        if allowed == "*" {
            return true
        }
        allowedLower := strings.ToLower(allowed)
        if allowedLower == modelLower {
            return true
        }
        if strings.HasSuffix(allowedLower, "*") && strings.HasPrefix(modelLower, allowedLower[:len(allowedLower)-1]) {
            return true
        }
    }

    return false
}

var validModuleSet = map[string]bool{
    "chat":   true,
    "code":   true,
    "design": true,
    "rag":    true,
    "agent":  true,
}

func ValidModule(module string) bool {
    return validModuleSet[module]
}

func CheckModelModuleAccess(r *http.Request, module string) bool {
    p := PrincipalFromContext(r.Context())
    if p == nil || p.IsMaster {
        return true
    }

    if len(p.ModelModules) == 0 {
        return true
    }

    for _, allowed := range p.ModelModules {
        if allowed == "*" || allowed == module {
            return true
        }
    }

    return false
}

func CheckBackendAccess(r *http.Request, backend string) bool {
    p := PrincipalFromContext(r.Context())
    if p == nil || p.IsMaster {
        return true
    }
    if p.KeyConfig == nil || len(p.KeyConfig.AllowedBackends) == 0 {
        return true
    }
    for _, allowed := range p.KeyConfig.AllowedBackends {
        if allowed == "*" || allowed == backend {
            return true
        }
    }
    return false
}

func extractAPIKey(r *http.Request) string {
    auth := r.Header.Get("Authorization")
    if strings.HasPrefix(auth, "Bearer ") {
        return strings.TrimPrefix(auth, "Bearer ")
    }

    if key := r.Header.Get("x-api-key"); key != "" {
        return key
    }

    return ""
}
