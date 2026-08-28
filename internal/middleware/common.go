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
        // R12 (audit): stamp the resolved id back onto the inbound request
        // header so downstream code reading r.Header.Get("X-Request-ID") —
        // notably adapter.WithFusionHeaders' passthrough list — sees the same
        // value the ctx carries and the response echoes. Without this, a
        // client that omits X-Request-ID got a generated id in ctx/response
        // but it never propagated to fusion-mlx/cloud upstreams (log
        // correlation gap). Set is idempotent when the client already sent it.
        r.Header.Set("X-Request-ID", id)
        ctx := context.WithValue(r.Context(), RequestIDKey, id)
        next.ServeHTTP(w, r.WithContext(ctx))
    })
}

// RequestIDFromContext extracts the X-Request-ID injected by RequestID. Used
// by the agent task registry (#102 ADR-001 sub-task 4) as the cancel task-id.
// Returns "" when no RequestID middleware ran.
func RequestIDFromContext(ctx context.Context) string {
    id, _ := ctx.Value(RequestIDKey).(string)
    return id
}

// InjectRequestID sets the X-Request-ID value in ctx the same way the RequestID
// middleware does, for callers that bypass the middleware (e.g. direct handler
// invocation in tests, or internal pipelines). Production paths should run the
// RequestID middleware instead.
func InjectRequestID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, RequestIDKey, id)
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
