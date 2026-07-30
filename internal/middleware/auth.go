package middleware

import (
    "crypto/subtle"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

type contextKey string

const (
    RequestIDKey contextKey = "request_id"
    AuthKeyKey   contextKey = "auth_key"
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

            matched := false
            for _, allowed := range cfg.APIKeys {
                if subtle.ConstantTimeCompare([]byte(allowed.Key), []byte(key)) == 1 {
                    matched = true
                    break
                }
            }

            if !matched {
                slog.Warn("invalid api key", "path", r.URL.Path, "remote", r.RemoteAddr)
                http.Error(w, `{"error":{"message":"Invalid API key","type":"auth_error"}}`, http.StatusUnauthorized)
                return
            }

            next.ServeHTTP(w, r)
        })
    }
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
