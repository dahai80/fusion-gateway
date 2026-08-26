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

            if keyCfg.BudgetLimit <= 0 && keyCfg.DailyBudgetLimit <= 0 {
                next.ServeHTTP(w, r)
                return
            }

            if st == nil {
                next.ServeHTTP(w, r)
                return
            }

            used, _, exceeded, err := st.CheckQuota(keyCfg.Name)
            if err != nil {
                // AH5 (audit P0): fail-closed. A quota-store error (Redis
                // disconnect, memory-store corruption) previously allowed the
                // request through ("budget check failed, allowing request"), so
                // a BudgetLimit=100 key could consume unboundedly during a
                // store outage — billing bypass. Commercial safety gates must
                // fail closed: when we cannot verify the budget, refuse rather
                // than risk unlimited spend. 503 (not 403) signals a transient
                // infrastructure fault the client may retry, distinct from a
                // hard quota-exceeded 403. The master key is unaffected — it
                // has no keyCfg.Name budget path (Name=="master" has no
                // BudgetLimit, so the BudgetLimit<=0 guard above already passed
                // it through before reaching here for non-budgeted keys).
                slog.Error("budget check failed, refusing request (fail-closed)",
                    "key", keyCfg.Name, "error", err)
                http.Error(w, `{"error":{"message":"Budget quota check unavailable","type":"quota_error"}}`, http.StatusServiceUnavailable)
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
