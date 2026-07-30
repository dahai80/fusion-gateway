package middleware

import (
    "context"
    "fmt"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/google/uuid"
)

func RequestID(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        id := r.Header.Get("X-Request-ID")
        if id == "" {
            id = uuid.New().String()
        }
        w.Header().Set("X-Request-ID", id)
        ctx := context.WithValue(r.Context(), RequestIDKey, id)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

func ConfigSnapshot(snap *config.ConfigSnapshot) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx := config.WithSnapshot(r.Context(), snap)
            w.Header().Set("X-Config-Version", fmt.Sprintf("%d", snap.Version))
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func CORS(cfg *config.CORSConfig) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if len(cfg.AllowedOrigins) > 0 {
                origin := "*"
                if cfg.AllowedOrigins[0] != "*" {
                    origin = cfg.AllowedOrigins[0]
                }
                w.Header().Set("Access-Control-Allow-Origin", origin)
            }

            if len(cfg.AllowedMethods) > 0 {
                methods := ""
                for i, m := range cfg.AllowedMethods {
                    if i > 0 {
                        methods += ", "
                    }
                    methods += m
                }
                w.Header().Set("Access-Control-Allow-Methods", methods)
            }

            if len(cfg.AllowedHeaders) > 0 {
                headers := ""
                for i, h := range cfg.AllowedHeaders {
                    if i > 0 {
                        headers += ", "
                    }
                    headers += h
                }
                w.Header().Set("Access-Control-Allow-Headers", headers)
            }

            if r.Method == http.MethodOptions {
                w.WriteHeader(http.StatusNoContent)
                return
            }

            next.ServeHTTP(w, r)
        })
    }
}
