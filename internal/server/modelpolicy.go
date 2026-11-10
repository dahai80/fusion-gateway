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
    "strings"

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
    // The admin-JWT / shell-session bridges only run inside withAdminOnly
    // (admin_handlers.go); without them here, a deployment with no OIDC
    // would have NO principal able to administer the policy. Bridge once
    // and re-check — fail-closed on an invalid token (same contract as
    // withAdminOnly).
    r = s.bridgeShellSession(bridgeAdminJWT(r, s.adminAuth))
    if !middleware.IsAdmin(r.Context()) {
        slog.Warn("model policy rejected for non-admin principal", "method", r.Method, "path", r.URL.Path,
            "subject", middleware.GetOIDCSubject(r.Context()))
        writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin role required for model policy"})
        return
    }
    tenant := adapter.TenantFromContext(r.Context())
    // #599: a PUT/GET that names a tenant the credential does not resolve
    // to used to be a silent mis-write (admin JWT with no team binding
    // always landed in the "" namespace). Fail loud instead.
    if reqTenant := r.URL.Query().Get("tenant"); reqTenant != "" && reqTenant != tenant {
        slog.Warn("model policy tenant mismatch", "resolved", tenant, "requested", reqTenant, "method", r.Method)
        writeJSON(w, http.StatusBadRequest, map[string]string{
            "error": "tenant mismatch: credential resolves to " + tenant + ", request named " + reqTenant,
        })
        return
    }
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
    // Same bridge as handleModelPolicy: shell-session admins resolve under
    // their admin role; the subject then reflects the session user.
    r = s.bridgeShellSession(bridgeAdminJWT(r, s.adminAuth))
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

// modelPolicyAllowsModel reports whether the tenant model policy permits
// calling chat with the given model directly (issue #11). #599 fallback
// chain: tenant policy → global default policy (the "" namespace) → deny.
// A tenant with no configured policy no longer hard-denies everything — it
// degrades to the global default, so a newly created tenant works without
// an admin hand-configuring it first. Match semantics are parity with the
// key-plane allowed_models matcher: exact (case-insensitive) or trailing-*
// prefix, so `user_allowlist: ["*"]` means "allow all" instead of silently
// matching nothing.
func (s *Server) modelPolicyAllowsModel(tenant, model string) bool {
    if s.modelPolicies == nil {
        return false
    }
    policy := s.modelPolicies.Policy(tenant)
    if !s.modelPolicies.HasPolicy(tenant) {
        policy = s.modelPolicies.Policy("")
    }
    if len(policy.UserAllowlist) == 0 {
        return false
    }
    found := false
    for _, allowed := range policy.UserAllowlist {
        if allowlistPatternMatches(allowed, model) {
            found = true
            break
        }
    }
    if !found {
        return false
    }
    if policy.PrivacyMode == modelpolicy.PrivacyModeLocalOnly && !s.modelPolicyIsLocal(model) {
        return false
    }
    return true
}

// allowlistPatternMatches is the #599 pattern matcher shared by the
// model-policy user_allowlist: case-insensitive exact match, or a
// trailing-`*` prefix match — the same shape as the key-plane
// allowed_models matcher (middleware.CheckModelAllowlist).
func allowlistPatternMatches(allowed, model string) bool {
    allowedLower := strings.ToLower(allowed)
    modelLower := strings.ToLower(model)
    if allowedLower == "*" {
        return true
    }
    if strings.HasSuffix(allowedLower, "*") {
        return strings.HasPrefix(modelLower, allowedLower[:len(allowedLower)-1])
    }
    return allowedLower == modelLower
}

// handleShellModelPolicy serves GET /api/v1/shell/model-policy (and the
// /api/v1/model-policy alias) behind withShellSession (#487, #491):
// shell-session modules resolve the same four-layer chain the /v1 plane
// serves, plus the effective allowlist and privacy_mode. local_only hides
// cloud models from BOTH the resolution and the surfaced allowlist.
// Consumers treat this best-effort (404/unreachable = fall through to the
// manifest suggested_model, #491 contract), so the payload is a superset:
// resolved_model, source, allowlist, privacy_mode, tenant, user, module.
func (s *Server) handleShellModelPolicy(w http.ResponseWriter, r *http.Request) {
    if s.modelPolicies == nil {
        writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "model policy store unavailable"})
        return
    }
    if r.Method != http.MethodGet {
        w.Header().Set("Allow", "GET")
        writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
        return
    }
    claims := shellClaimsFrom(r.Context())
    user := claims.Sub
    tenant := claims.TenantID
    if tenant == "" {
        tenant = "local"
    }
    module := r.URL.Query().Get("module")
    p := s.modelPolicies.Policy(tenant)
    d := s.modelPolicies.Resolve(tenant, user, module, s.modelPolicyFallback(), s.modelPolicyIsLocal)
    privacyMode := p.PrivacyMode
    if privacyMode == "" {
        privacyMode = modelpolicy.PrivacyModeAll
    }
    allowlist := p.UserAllowlist
    if privacyMode == modelpolicy.PrivacyModeLocalOnly && allowlist != nil {
        filtered := make([]string, 0, len(allowlist))
        for _, m := range allowlist {
            if s.modelPolicyIsLocal(m) {
                filtered = append(filtered, m)
            }
        }
        allowlist = filtered
    }
    writeJSON(w, http.StatusOK, map[string]any{
        "resolved_model": d.Model,
        "source":         d.Source,
        "allowlist":      allowlist,
        "privacy_mode":   privacyMode,
        "privacy_filtered": d.PrivacyFiltered,
        "tenant":         tenant,
        "user":           user,
        "module":         module,
    })
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
