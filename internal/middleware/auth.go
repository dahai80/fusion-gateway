package middleware

import (
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

type keyLookupStore interface {
    GetKeyByHash(hash string) (*store.APIKeyEntry, error)
    // GetTeamByKey resolves the tenant bound to a plaintext api key. Issue
    // #150 Gap1: the key->team binding lives in the store side-table but was
    // never consulted on the request path — a tenant-A key could reach
    // tenant-B's data via a spoofed X-Space-Id. Wired here so the gateway
    // derives an authoritative tenant from the credential itself.
    GetTeamByKey(apiKey string) (*store.Team, error)
}

func APIKeyAuth(cfg *config.AuthConfig) func(http.Handler) http.Handler {
    return APIKeyAuthWithStore(cfg, nil)
}

func APIKeyAuthWithStore(cfg *config.AuthConfig, st keyLookupStore) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx, p := EnsurePrincipal(r.Context())

            // withAdminOnly may have already bridged an admin-login JWT into a
            // Principal{AuthMethod:"admin-jwt"} (server/admin_handlers.go). The
            // API-key chain does not own that identity — skip key resolution so
            // it neither overwrites the bridged role nor 401-rejects a request
            // whose Bearer is an admin JWT rather than an API key. The admin
            // route's own IsAdmin gate has already authorized it.
            if p.AuthMethod == "admin-jwt" {
                next.ServeHTTP(w, r.WithContext(ctx))
                return
            }

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
            // #150 Gap1: stamp the gateway-derived tenant onto the Principal so
            // downstream code (and the outbound X-Fusion-Tenant header) uses the
            // credential's binding, never a client-supplied X-Space-Id. RBAC
            // middleware may have already set p.Team from an OIDC claim — only
            // fill it when empty so the stronger OIDC-bound identity wins.
            if matchedKey.TeamID != "" && p.Team == nil {
                ti := &TeamInfo{
                    ID:   matchedKey.TeamID,
                    Name: matchedKey.TeamID,
                    Role: RoleInference,
                }
                // #159: carry the tenant's Tier tag so the 3-tier priority
                // queue can admit by tenant priority class. Best-effort: a
                // store miss leaves Tier empty (defaults to general via the
                // coarse-intent heuristic).
                if team, terr := st.GetTeamByKey(key); terr == nil && team != nil {
                    ti.Tier = team.Tier
                }
                p.Team = ti
            }
            // Attach the tenant to the ctx so adapter.InjectFusionHeaders
            // stamps X-Fusion-Tenant on every outbound upstream request.
            ctx = adapter.WithTenant(ctx, matchedKey.TeamID)
            slog.Debug("api key authenticated", "key", matchedKey.Name, "modules", matchedKey.ModelModules, "team", matchedKey.TeamID)
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
    // #150 Gap1: derive the tenant from the credential's key->team binding.
    // The plaintext key is in hand here, and GetTeamByKey's side-table is
    // keyed by plaintext. A missing binding (legacy key, master key handled
    // above) is not an error — kc.TeamID stays empty and no tenant is
    // asserted downstream. A store error is logged but does not block auth
    // (fail-open on tenant metadata, fail-closed is the key lookup above);
    // the downstream X-Fusion-Tenant injection simply omits the header.
    if team, terr := st.GetTeamByKey(key); terr != nil {
        slog.Debug("no team binding for api key (tenant header omitted)",
            "key", entry.Name, "error", terr)
    } else if team != nil {
        kc.TeamID = team.ID
        slog.Debug("tenant resolved from api key binding",
            "key", entry.Name, "team", team.ID)
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
