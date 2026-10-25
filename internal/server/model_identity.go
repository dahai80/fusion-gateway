package server

import (
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/router"
)

// resolveModelIdentity expands models.aliases and "backend/model"
// qualified ids against the current config snapshot (plan §13.2/§13.4).
// Returns the resolution; callers rewrite their request model to Bare and
// stamp the original qualified/alias form into the request log for
// traceability. No-op (Bare == input, Backend == "") when the client sent
// a plain bare model with no alias entry.
func (s *Server) resolveModelIdentity(raw string) config.ResolvedModel {
    snap := s.cfg
    res := config.ResolveModel(raw, snap.Config.Backends, snap.Config.Models)
    if len(res.AliasHops) > 0 {
        slog.Info("model alias resolved",
            "requested", res.Requested,
            "resolved", res.Bare,
            "hops", strings.Join(res.AliasHops, ","),
            "config_version", snap.Version,
        )
    }
    if res.Backend != "" {
        slog.Info("qualified model id resolved",
            "requested", res.Requested,
            "bare", res.Bare,
            "backend", res.Backend,
        )
    }
    return res
}

// stampModelIdentity records the client-facing model form on the request
// log entry: entry.Model keeps the bare id the engine/upstream saw (set by
// the existing handler paths), ModelMapped carries the original qualified
// or alias form when it differs (plan §13.2 — reports aggregate on the
// bare name and disambiguate on ModelMapped/ChannelName).
func stampModelIdentity(r *http.Request, res config.ResolvedModel) {
    if res.Requested == res.Bare {
        return
    }
    entry := middleware.GetRequestLog(r.Context())
    if entry == nil {
        return
    }
    entry.ModelMapped = res.Requested
}

// applyQualifiedModelLock pins the routing decision to the backend named
// in a qualified model id (plan §13.2). Deterministic: the engine's chain
// result is overridden (engine reason preserved in the log), EXCEPT the
// cloud_models allowlist still outranks the pin — a pinned cloud backend
// for an allowlist-pinned-local model keeps the engine's local decision
// (AC17: the guard is a safety rail, not a suggestion). A pinned backend
// missing from the pool is a hard 503-style rejection rather than a
// silent divert to another provider.
func (s *Server) applyQualifiedModelLock(res config.ResolvedModel, decision *router.RouteDecision) *router.RouteDecision {
    if decision == nil || res.Backend == "" {
        return decision
    }

    if s.pool.IsLocalProvider(res.Backend) {
        // Local pin: the handler's LocalBackend case resolves the local
        // provider, so delegate to the standard local decision shape.
        if decision.Backend == router.LocalBackend {
            // Engine already chose local; keep it but make the pin visible.
            decision.Reason = "qualified_lock:" + decision.Reason
            return decision
        }
        slog.Info("qualified model lock overrides engine decision to local",
            "qualified", res.Qualified(),
            "engine_backend", string(decision.Backend),
            "engine_reason", decision.Reason,
        )
        return &router.RouteDecision{
            Backend: router.LocalBackend,
            Reason:  "qualified_lock:" + decision.Reason,
        }
    }

    // Cloud pin. The cloud_models allowlist (P0.15) outranks the pin:
    // allowlist-pinned-local models stay local even when qualified to a
    // cloud backend (AC17).
    if !router.ModelAllowedByCloudList(res.Bare, s.cfg.Config.Routing.CloudModels) {
        slog.Warn("qualified model lock to cloud blocked by cloud_models allowlist, keeping engine decision",
            "qualified", res.Qualified(),
            "engine_backend", string(decision.Backend),
        )
        return decision
    }

    // Pool membership check: a qualified id naming a backend that is not
    // built (unknown name, disabled) must fail loudly, not divert.
    if _, ok := s.pool.Get(res.Backend); !ok {
        slog.Error("qualified model lock targets unknown/disabled backend, rejecting",
            "qualified", res.Qualified(),
            "known_backends", len(s.pool.ListProviders()),
        )
        return &router.RouteDecision{
            Backend:  router.CloudBackend,
            Reason:   "qualified_lock_unknown_backend",
            Rejected: true,
            RejectDetail: `{"error":{"message":"qualified model references a backend that is not configured or enabled","type":"invalid_request"}}`,
        }
    }

    if decision.Backend == router.CloudBackend && decision.CloudTarget == res.Backend {
        decision.Reason = "qualified_lock:" + decision.Reason
        return decision
    }
    slog.Info("qualified model lock overrides engine decision to cloud backend",
        "qualified", res.Qualified(),
        "engine_backend", string(decision.Backend),
        "engine_reason", decision.Reason,
    )
    return &router.RouteDecision{
        Backend:     router.CloudBackend,
        Reason:      "qualified_lock:" + decision.Reason,
        CloudTarget: res.Backend,
    }
}
