package server

// #486 model-policy plane: tenant-scoped policy administration and
// effective-model resolution. Both endpoints ride the standard /v1
// middleware (auth, rate limit, principal), then enforce their own role /
// plane rules:
//   - /v1/model-policy (GET/PUT): admin role only (middleware.IsAdmin). The
//     inference master key maps to RoleInference (RR3) and is deliberately
//     rejected — policy administration is a management-plane action.
//   - /v1/model-preference (GET/PUT): any authenticated principal; PUT
//     writes the caller's OWN preference, allowlist-constrained by the
//     store.
//
// The tenant comes from the authoritative gateway derivation
// (adapter.TenantFromContext: api_key→team binding, identity lease) — a
// client-supplied header is never trusted. A principal with no tenant
// binding resolves in the "" tenant namespace, which is fully isolated
// from named tenants.

import (
    "encoding/json"
    "errors"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/modelpolicy"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// handleModelPolicy serves GET/PUT /v1/model-policy (#486 scope 1).
func (s *Server) handleModelPolicy(w http.ResponseWriter, r *http.Request) {
    if s.modelPolicies == nil {
        writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "model policy store unavailable"})
        return
    }
    if !middleware.IsAdmin(r.Context()) {
        slog.Warn("model policy rejected for non-admin principal", "method", r.Method, "path", r.URL.Path,
            "subject", middleware.GetOIDCSubject(r.Context()))
        writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required for model policy"})
        return
    }
    tenant := adapter.TenantFromContext(r.Context())
    switch r.Method {
    case http.MethodGet:
        writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant, "policy": s.modelPolicies.Policy(tenant)})
    case http.MethodPut:
        var p modelpolicy.Policy
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid policy body"})
            return
        }
        if err := s.modelPolicies.SetPolicy(tenant, p); err != nil {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
            return
        }
        s.recordModelPolicyAudit(r, middleware.GetRBACRole(r.Context()), AuditModelPolicyActionUpsert, http.MethodPut, http.StatusOK, tenant, "")
        writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant, "policy": s.modelPolicies.Policy(tenant)})
    default:
        w.Header().Set("Allow", "GET, PUT")
        writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
    }
}

// modelPreferenceRequest is the PUT body for /v1/model-preference.
type modelPreferenceRequest struct {
    Module string `json:"module"`
    Model  string `json:"model"`
}

// handleModelPreference serves GET/PUT /v1/model-preference (#486 scope 1).
// GET resolves the effective model for (tenant, caller, module) following
// the four-layer chain; PUT records the caller's own preference.
func (s *Server) handleModelPreference(w http.ResponseWriter, r *http.Request) {
    if s.modelPolicies == nil {
        writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "model policy store unavailable"})
        return
    }
    principal := middleware.PrincipalFromContext(r.Context())
    user := "anonymous"
    if principal != nil {
        user = principal.Subject()
    }
    tenant := adapter.TenantFromContext(r.Context())
    switch r.Method {
    case http.MethodGet:
        module := r.URL.Query().Get("module")
        d := s.modelPolicies.Resolve(tenant, user, module, s.modelPolicyFallback(), s.modelPolicyIsLocal)
        writeJSON(w, http.StatusOK, map[string]any{
            "tenant": tenant, "user": user, "module": module,
            "model": d.Model, "source": d.Source, "privacy_filtered": d.PrivacyFiltered,
        })
    case http.MethodPut:
        var req modelPreferenceRequest
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
            writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid preference body"})
            return
        }
        if err := s.modelPolicies.SetUserPreference(tenant, user, req.Module, req.Model); err != nil {
            status := http.StatusBadRequest
            if errors.Is(err, modelpolicy.ErrNotInAllowlist) {
                status = http.StatusForbidden
            }
            writeJSON(w, status, map[string]string{"error": err.Error()})
            return
        }
        s.recordModelPolicyAudit(r, middleware.GetRBACRole(r.Context()), AuditModelPolicyActionPrefSet, http.MethodPut, http.StatusOK, tenant, req.Module+"="+req.Model)
        writeJSON(w, http.StatusOK, map[string]any{
            "tenant": tenant, "user": user, "module": req.Module, "model": req.Model,
        })
    case http.MethodDelete:
        module := r.URL.Query().Get("module")
        s.modelPolicies.DeleteUserPreference(tenant, user, module)
        s.recordModelPolicyAudit(r, middleware.GetRBACRole(r.Context()), AuditModelPolicyActionPrefClear, http.MethodDelete, http.StatusOK, tenant, module)
        writeJSON(w, http.StatusOK, map[string]any{"tenant": tenant, "user": user, "module": module})
    default:
        w.Header().Set("Allow", "GET, PUT, DELETE")
        writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
    }
}

// modelPolicyFallback is the legacy build-time last link of the chain
// (routing.default_model). Nil snapshot degrades to no fallback.
func (s *Server) modelPolicyFallback() string {
    if snap := s.snapshot(); snap != nil {
        return snap.Config.Routing.DefaultModel
    }
    return ""
}

// modelPolicyIsLocal reports local-backend residency via the router's
// ModelSet wiring. Unwired router = nothing is local (fail-closed for a
// local_only pin).
func (s *Server) modelPolicyIsLocal(model string) bool {
    if s.router == nil {
        return false
    }
    return s.router.IsLocalModel(model)
}

// Audit action classes for the model-policy plane (shell audit-log family,
// hash-chained; stored at data_dir/model_policy_audit.json).
const (
    AuditModelPolicyActionUpsert   = "model_policy_upsert"
    AuditModelPolicyActionPrefSet  = "model_preference_set"
    AuditModelPolicyActionPrefClear = "model_preference_clear"
)

// recordModelPolicyAudit appends to the hash-chained audit log; audit
// failures never fail the request (same contract as the shell surfaces).
func (s *Server) recordModelPolicyAudit(r *http.Request, role middleware.Role, action string, method string, status int, tenant, detail string) {
    if s.modelPolicyAudit == nil {
        return
    }
    user := adapter.TenantFromContext(r.Context()) + "/" + middleware.GetOIDCSubject(r.Context())
    outcome := shell.AuditOutcomeOk
    if status >= 400 {
        outcome = shell.AuditOutcomeDenied
    }
    if err := s.modelPolicyAudit.Record(user, string(role), shell.AuditClassConfig, method, action, outcome, 0); err != nil {
        slog.Warn("model policy audit record failed", "action", action, "error", err)
    }
    slog.Info("model policy change", "action", action, "tenant", tenant, "detail", detail, "status", status)
}
