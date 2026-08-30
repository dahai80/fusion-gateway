package server

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/admin"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

func (s *Server) withAdminOnly(handler http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        // RR3 (audit P0): admin surface requires an admin role, NOT the
        // inference MasterKey. The inference and management planes must stay
        // separated — a leaked inference key must never reach config mutation,
        // GC, or team/org CRUD. IsAdmin delegates to EffectiveRole, which (post
        // RR3) maps master to RoleInference, so this rejects master. The extra
        // IsMasterKey guard is defense-in-depth: even if a future change re-maps
        // master toward admin, this explicit reject keeps the plane boundary.
        if middleware.IsMasterKey(r.Context()) {
            slog.Warn("admin endpoint rejected inference master key",
                "path", r.URL.Path, "method", r.Method,
                "note", "admin surface requires admin JWT, not inference master key")
            http.Error(w, `{"error":{"message":"Admin access required","type":"rbac_error"}}`, http.StatusForbidden)
            return
        }
        // Bridge an admin-login JWT into middleware.Principal BEFORE the IsAdmin
        // check. The admin module issues its own JWT (AdminClaims{Role:"admin"})
        // via /admin/api/login; the admin module's own routes validate it through
        // requireAdminRole -> GetAdminClaims. But withAdminOnly checks
        // middleware.IsAdmin -> PrincipalFromContext, and the withMiddleware
        // chain (APIKeyAuth/OIDC/RBAC) never parses the admin JWT into Principal
        // — so a valid admin Bearer reached 403 on every withAdminOnly route
        // (/v1/browser/nodes, /admin/gc, /admin/teams, /admin/orgs, ...). This
        // bridge validates the Bearer as an admin JWT once and sets
        // Principal{Role:RoleAdmin, AuthMethod:"admin-jwt"}; the chain below
        // (withMiddleware -> APIKeyAuth/RBAC) honors the preset role instead of
        // overwriting it (see AuthMethod short-circuits in auth.go + rbac.go).
        // Fail-closed: an invalid/non-admin token leaves Principal untouched and
        // the IsAdmin check below denies. The master-key reject above still
        // fires first, so an inference key never reaches here as admin.
        r = bridgeAdminJWT(r, s.adminAuth)
        if !middleware.IsAdmin(r.Context()) {
            slog.Warn("admin endpoint denied: not admin",
                "path", r.URL.Path, "method", r.Method)
            http.Error(w, `{"error":{"message":"Admin access required","type":"rbac_error"}}`, http.StatusForbidden)
            return
        }
        s.withMiddleware(handler)(w, r)
    }
}

// bridgeAdminJWT validates an Authorization: Bearer admin-login JWT and, when
// valid, attaches a Principal{Role:RoleAdmin, AuthMethod:"admin-jwt"} to the
// request context so withAdminOnly's IsAdmin check passes. Returns the request
// unchanged (no Principal set) when there is no Bearer, the token is not an
// admin JWT, or admin auth is not configured — the caller then denies via
// IsAdmin. Non-fatal on adminAuth disabled (Enabled()==false) so a deployment
// without the admin module still fails closed at IsAdmin rather than panicking.
func bridgeAdminJWT(r *http.Request, adminAuth *admin.AdminAuth) *http.Request {
    if adminAuth == nil || !adminAuth.Enabled() {
        return r
    }
    authz := r.Header.Get("Authorization")
    if !strings.HasPrefix(authz, "Bearer ") {
        return r
    }
    tokenStr := strings.TrimPrefix(authz, "Bearer ")
    claims, err := adminAuth.ValidateToken(tokenStr)
    if err != nil || claims == nil {
        return r
    }
    // Only the admin role (set by login.go GenerateToken(_, "admin")) grants the
    // admin plane. A token claiming a different role is not elevated.
    if claims.Role != "admin" {
        slog.Warn("admin endpoint rejected admin JWT with non-admin role",
            "path", r.URL.Path, "method", r.Method, "role", claims.Role)
        return r
    }
    ctx := middleware.ContextWithPrincipal(r.Context(), &middleware.Principal{
        AuthMethod: "admin-jwt",
        Role:       middleware.RoleAdmin,
    })
    slog.Debug("admin JWT bridged to Principal", "path", r.URL.Path, "username", claims.Username)
    return r.WithContext(ctx)
}

func (s *Server) handleAdminTeams(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        teams, err := s.store.ListTeams()
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
            return
        }
        writeJSON(w, http.StatusOK, teams)
    case http.MethodPost:
        var team store.Team
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&team); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if team.ID == "" {
            http.Error(w, `{"error":{"message":"Team ID is required","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if err := s.store.CreateTeam(&team); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusConflict)
            return
        }
        writeJSON(w, http.StatusCreated, team)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleAdminTeamsCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/admin/teams/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Team ID required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        team, err := s.store.GetTeam(id)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, team)
    case http.MethodPut:
        var team store.Team
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&team); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        team.ID = id
        if err := s.store.UpdateTeam(&team); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, team)
    case http.MethodDelete:
        if err := s.store.DeleteTeam(id); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
            return
        }
        w.WriteHeader(http.StatusNoContent)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleAdminOrgs(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodGet:
        orgs, err := s.store.ListOrgs()
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
            return
        }
        writeJSON(w, http.StatusOK, orgs)
    case http.MethodPost:
        var org store.Organization
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&org); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if org.ID == "" {
            http.Error(w, `{"error":{"message":"Organization ID is required","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        if err := s.store.CreateOrg(&org); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusConflict)
            return
        }
        writeJSON(w, http.StatusCreated, org)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleAdminOrgsCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/admin/orgs/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Organization ID required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        org, err := s.store.GetOrg(id)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, org)
    case http.MethodDelete:
        if err := s.store.DeleteOrg(id); err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
            return
        }
        w.WriteHeader(http.StatusNoContent)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(v); err != nil {
        slog.Error("failed to encode json response", "error", err)
    }
}

func (s *Server) handleBatches(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodPost:
        var req struct {
            Requests         []store.BatchRequest `json:"requests"`
            Endpoint         string               `json:"endpoint"`
            CompletionWindow string               `json:"completion_window"`
        }
        if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminBodySize)).Decode(&req); err != nil {
            http.Error(w, `{"error":{"message":"Invalid request body","type":"invalid_request"}}`, http.StatusBadRequest)
            return
        }
        // M1 (audit): no batch worker exists — the prior path stored the
        // submission with status=pending and returned 200, silently accepting
        // work that was never executed (internal/batch package + ProcessFn +
        // go s.process(b) were deleted in 32a1217 and never replaced). A
        // commercial release must not advertise execution it cannot perform.
        // 501 is the OpenAI-correct "endpoint exists, operation unsupported"
        // signal; submissions are still validated above so malformed input
        // gets a precise 400. GET list / per-item CRUD stay (harmless on an
        // empty store). When a worker is wired, restore the CreateBatch path.
        slog.Warn("batch create rejected: no worker implemented, endpoint not available for commercial release",
            "endpoint", req.Endpoint, "requests", len(req.Requests))
        writeJSON(w, http.StatusNotImplemented, map[string]interface{}{
            "error": map[string]interface{}{
                "message": "batch execution is not implemented; the endpoint accepts and validates submissions but no worker drains them. Use individual /v1/chat/completions requests.",
                "type":   "not_implemented",
            },
        })
    case http.MethodGet:
        batches, err := s.store.ListBatches()
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"server_error"}}`, err.Error()), http.StatusInternalServerError)
            return
        }
        writeJSON(w, http.StatusOK, batches)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}

func (s *Server) handleBatchCRUD(w http.ResponseWriter, r *http.Request) {
    id := strings.TrimPrefix(r.URL.Path, "/v1/batches/")
    if id == "" {
        http.Error(w, `{"error":{"message":"Batch ID required","type":"invalid_request"}}`, http.StatusBadRequest)
        return
    }
    switch r.Method {
    case http.MethodGet:
        b, err := s.store.GetBatch(id)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"not_found"}}`, err.Error()), http.StatusNotFound)
            return
        }
        writeJSON(w, http.StatusOK, b)
    case http.MethodPost:
        if strings.HasSuffix(r.URL.Path, "/cancel") {
            b, err := s.store.CancelBatch(id)
            if err != nil {
                http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
                return
            }
            writeJSON(w, http.StatusOK, b)
            return
        }
        http.Error(w, `{"error":{"message":"Unknown action","type":"invalid_request"}}`, http.StatusBadRequest)
    default:
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
    }
}
