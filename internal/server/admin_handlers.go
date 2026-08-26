package server

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"

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
        if !middleware.IsAdmin(r.Context()) {
            http.Error(w, `{"error":{"message":"Admin access required","type":"rbac_error"}}`, http.StatusForbidden)
            return
        }
        s.withMiddleware(handler)(w, r)
    }
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
        b, err := s.store.CreateBatch(req.Requests, req.Endpoint, req.CompletionWindow)
        if err != nil {
            http.Error(w, fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request"}}`, err.Error()), http.StatusBadRequest)
            return
        }
        writeJSON(w, http.StatusOK, b)
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
