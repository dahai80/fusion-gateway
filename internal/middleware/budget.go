package middleware

import (
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func BudgetBlock(st store.Store) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            keyCfg := GetAuthKeyConfig(r.Context())
            if keyCfg == nil || keyCfg.Name == "" {
                next.ServeHTTP(w, r)
                return
            }

            if keyCfg.BudgetLimit <= 0 {
                next.ServeHTTP(w, r)
                return
            }

            if st == nil {
                next.ServeHTTP(w, r)
                return
            }

            used, _, exceeded, err := st.CheckQuota(keyCfg.Name)
            if err != nil {
                slog.Warn("budget check failed, allowing request", "key", keyCfg.Name, "error", err)
                next.ServeHTTP(w, r)
                return
            }

            if exceeded {
                slog.Warn("budget exceeded, blocking request",
                    "key", keyCfg.Name,
                    "used", used,
                )
                http.Error(w, `{"error":{"message":"Budget quota exceeded","type":"quota_error"}}`, http.StatusForbidden)
                return
            }

            next.ServeHTTP(w, r)
        })
    }
}
