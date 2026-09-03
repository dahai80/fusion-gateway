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

            // #159: per-tenant (team) daily quota check. The gateway is the
            // quota authority above multi-node (which stays Redis-free); this
            // gate enforces a tenant's DailyQuotaLimit before a request enters
            // the inference pool. Fail-closed mirrors the per-key path. Only
            // checked when a team is bound and configured a daily cap; the
            // cumulative QuotaLimit cap is still owned by CheckTeamQuota.
            if p := PrincipalFromContext(r.Context()); p != nil && p.Team != nil && p.Team.ID != "" {
                _, _, teamOk, teamErr := st.CheckTeamQuota(p.Team.ID)
                if teamErr != nil {
                    slog.Error("tenant budget check failed, refusing request (fail-closed)",
                        "team", p.Team.ID, "error", teamErr)
                    http.Error(w, `{"error":{"message":"Tenant quota check unavailable","type":"quota_error"}}`, http.StatusServiceUnavailable)
                    return
                }
                if !teamOk {
                    slog.Warn("tenant budget exceeded, blocking request",
                        "team", p.Team.ID)
                    http.Error(w, `{"error":{"message":"Tenant quota exceeded","type":"quota_error"}}`, http.StatusForbidden)
                    return
                }
            }

            next.ServeHTTP(w, r)
        })
    }
}
