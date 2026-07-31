package middleware

import (
    "context"
    "crypto/subtle"
    "log/slog"
    "net/http"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func APIKeyAuth(cfg *config.AuthConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !cfg.Enabled || cfg.Passthrough {
                next.ServeHTTP(w, r)
                return
            }

            key := extractAPIKey(r)
            if key == "" {
                slog.Warn("missing api key", "path", r.URL.Path, "remote", r.RemoteAddr)
                http.Error(w, `{"error":{"message":"Missing API key","type":"auth_error"}}`, http.StatusUnauthorized)
                return
            }

            // MasterKey check
            if cfg.MasterKey != "" && subtle.ConstantTimeCompare([]byte(cfg.MasterKey), []byte(key)) == 1 {
                slog.Info("master key authenticated", "path", r.URL.Path)
                ctx := context.WithValue(r.Context(), IsMasterKeyKey, true)
                ctx = context.WithValue(ctx, AuthKeyConfigKey, &config.AuthKeyConfig{
                    Key:  cfg.MasterKey,
                    Name: "master",
                })
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

            if matchedKey == nil {
                slog.Warn("invalid api key", "path", r.URL.Path, "remote", r.RemoteAddr)
                http.Error(w, `{"error":{"message":"Invalid API key","type":"auth_error"}}`, http.StatusUnauthorized)
                return
            }

            // Expiry check
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

            ctx := context.WithValue(r.Context(), AuthKeyConfigKey, matchedKey)
            ctx = context.WithValue(ctx, IsMasterKeyKey, false)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func CheckModelAllowlist(r *http.Request, model string) bool {
    isMaster, _ := r.Context().Value(IsMasterKeyKey).(bool)
    if isMaster {
        return true
    }

    keyCfg, _ := r.Context().Value(AuthKeyConfigKey).(*config.AuthKeyConfig)
    if keyCfg == nil {
        return true
    }

    if len(keyCfg.AllowedModels) == 0 {
        return true
    }

    for _, allowed := range keyCfg.AllowedModels {
        if allowed == "*" || allowed == model {
            return true
        }
        if strings.HasSuffix(allowed, "*") && strings.HasPrefix(model, allowed[:len(allowed)-1]) {
            return true
        }
    }

    return false
}

func GetAuthKeyConfig(ctx context.Context) *config.AuthKeyConfig {
    cfg, _ := ctx.Value(AuthKeyConfigKey).(*config.AuthKeyConfig)
    return cfg
}

func IsMasterKey(ctx context.Context) bool {
    v, _ := ctx.Value(IsMasterKeyKey).(bool)
    return v
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
