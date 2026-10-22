package server

import (
    "encoding/json"
    "errors"
    "fmt"
    "log/slog"
    "net/http"
    "path/filepath"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// E6 admin surfaces (#275, fusion-web-shell#42): adapters vault / connectors
// / config overrides / audit / roles + menu matrix, session-gated under
// /api/v1/shell/* following the #271 keys precedent. Role gates: vault,
// connectors, config, roles, menus = admin; audit = admin or auditor.
// Every mutating handler records a row in the hash-chain audit log.

// setupShellAdminSurfaces constructs the E6 stores from store.data_dir.
// A store construction/load failure degrades that surface to nil (its
// handlers 503) rather than wedging the whole shell subsystem.
func (s *Server) setupShellAdminSurfaces(cfg *config.Config) {
    dataDir := ""
    if cfg.Store.DataDir != "" {
        dataDir = config.ExpandPath(cfg.Store.DataDir)
    }
    join := func(name string) string {
        if dataDir == "" {
            return ""
        }
        return filepath.Join(dataDir, name)
    }

    vault, err := shell.NewAdapterVault(join("shell_adapters.json"), cfg.Auth.MasterKey)
    if err != nil {
        slog.Error("shell adapter vault init failed — adapters surface disabled", "error", err)
    } else {
        if err := vault.Load(); err != nil {
            slog.Error("shell adapter vault load failed — starting empty", "error", err)
        }
        s.shellVault = vault
    }
    s.shellConnectors = shell.NewConnectorStore(join("shell_connectors.json"), s.shellVault)
    if err := s.shellConnectors.Load(); err != nil {
        slog.Error("shell connector store load failed — starting empty", "error", err)
        s.shellConnectors = shell.NewConnectorStore(join("shell_connectors.json"), s.shellVault)
    }
    s.shellOverrides = shell.NewConfigOverrides(join("shell_config_overrides.json"))
    if err := s.shellOverrides.Load(); err != nil {
        slog.Error("shell config overrides load failed — starting empty", "error", err)
        s.shellOverrides = shell.NewConfigOverrides(join("shell_config_overrides.json"))
    }
    s.shellAudit = shell.NewAuditLog(join("shell_audit.json"))
    if err := s.shellAudit.Load(); err != nil {
        slog.Error("shell audit store load failed — starting fresh", "error", err)
        s.shellAudit = shell.NewAuditLog(join("shell_audit.json"))
    }
    s.shellRoles = shell.NewRoleStore(join("shell_roles.json"))
    if err := s.shellRoles.Load(); err != nil {
        slog.Error("shell role store load failed — starting empty", "error", err)
        s.shellRoles = shell.NewRoleStore(join("shell_roles.json"))
    }
    s.shellMenus = shell.NewMenuPermissions(join("shell_menu_permissions.json"))
    if err := s.shellMenus.Load(); err != nil {
        slog.Error("shell menu permissions load failed — starting empty", "error", err)
        s.shellMenus = shell.NewMenuPermissions(join("shell_menu_permissions.json"))
    }
}

// registerShellAdminRoutes wires the E6 surface routes. Called from
// registerShellRoutes so everything stays behind shell.enabled.
func (s *Server) registerShellAdminRoutes(mux *http.ServeMux) {
    mux.HandleFunc("GET /api/v1/shell/adapters", s.withShellSession(requireShellAdmin(s.handleShellAdaptersList)))
    mux.HandleFunc("PUT /api/v1/shell/adapters/{platform}", s.withShellSession(requireShellAdmin(s.handleShellAdapterPut)))
    mux.HandleFunc("POST /api/v1/shell/adapters/{platform}/test", s.withShellSession(requireShellAdmin(s.handleShellAdapterTest)))
    mux.HandleFunc("GET /api/v1/shell/connectors", s.withShellSession(requireShellAdmin(s.handleShellConnectorsList)))
    mux.HandleFunc("PUT /api/v1/shell/connectors/{id}", s.withShellSession(requireShellAdmin(s.handleShellConnectorPut)))
    mux.HandleFunc("POST /api/v1/shell/connectors/{id}/test", s.withShellSession(requireShellAdmin(s.handleShellConnectorTest)))
    mux.HandleFunc("GET /api/v1/shell/config/{key}", s.withShellSession(requireShellAdmin(s.handleShellConfigGet)))
    mux.HandleFunc("PUT /api/v1/shell/config/{key}", s.withShellSession(requireShellAdmin(s.handleShellConfigPut)))
    mux.HandleFunc("GET /api/v1/shell/audit", s.withShellSession(requireShellRole("admin", "auditor")(s.handleShellAudit)))
    mux.HandleFunc("GET /api/v1/shell/roles", s.withShellSession(requireShellAdmin(s.handleShellRolesList)))
    mux.HandleFunc("POST /api/v1/shell/roles", s.withShellSession(requireShellAdmin(s.handleShellRoleCreate)))
    mux.HandleFunc("DELETE /api/v1/shell/roles/{name}", s.withShellSession(requireShellAdmin(s.handleShellRoleDelete)))
    mux.HandleFunc("GET /api/v1/shell/menu-permissions", s.withShellSession(requireShellAdmin(s.handleShellMenuPermissionsGet)))
    mux.HandleFunc("PUT /api/v1/shell/menu-permissions", s.withShellSession(requireShellAdmin(s.handleShellMenuPermissionsPut)))
}

// requireShellRole gates a handler behind any of the given roles (EI9: the
// check is server-side; the shell host renders what the API allows).
func requireShellRole(roles ...string) func(http.HandlerFunc) http.HandlerFunc {
    return func(next http.HandlerFunc) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
            claims := shellClaimsFrom(r.Context())
            for _, want := range roles {
                if hasAnyRole(claims.Roles, want) {
                    next(w, r)
                    return
                }
            }
            slog.Warn("shell admin surface denied (role)", "path", r.URL.Path, "user", claims.Sub, "roles", claims.Roles, "want", roles)
            shell.WriteEnvelope(w, http.StatusForbidden, "forbidden",
                fmt.Sprintf("one of roles %v required", roles), "", false)
        }
    }
}

// shellAuditRecord appends one hash-chain row for the session caller. Never
// fails the request — an audit write failure is logged loudly (the chain
// store already retried via write-through) but the admin operation stands.
func (s *Server) shellAuditRecord(r *http.Request, actionClass, outcome string, durationMS int64) {
    if s.shellAudit == nil {
        return
    }
    claims := shellClaimsFrom(r.Context())
    role := ""
    if len(claims.Roles) > 0 {
        role = claims.Roles[0]
    }
    if err := s.shellAudit.Record(claims.Sub, role, actionClass, r.Method, r.URL.Path, outcome, durationMS); err != nil {
        slog.Error("shell audit record failed", "path", r.URL.Path, "error", err)
    }
}

func writeShellJSON(w http.ResponseWriter, status int, v any) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(v)
}

// --- Adapters vault -------------------------------------------------------

func (s *Server) handleShellAdaptersList(w http.ResponseWriter, r *http.Request) {
    if s.shellVault == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "adapter vault is not initialized", "", false)
        return
    }
    writeShellJSON(w, http.StatusOK, map[string]any{"adapters": s.shellVault.List()})
}

func (s *Server) handleShellAdapterPut(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    if s.shellVault == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "adapter vault is not initialized", "", false)
        return
    }
    platform := r.PathValue("platform")
    var req struct {
        Credentials  map[string]string `json:"credentials"`
        RateLimitRPS float64           `json:"rate_limit_rps"`
        JitterMS     int               `json:"jitter_ms"`
        Enabled      bool              `json:"enabled"`
    }
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShellBody)).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode adapter: %v", err), platform, false)
        return
    }
    if err := s.shellVault.Put(platform, req.Credentials, req.RateLimitRPS, req.JitterMS, req.Enabled); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "adapter_rejected", err.Error(), platform, false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassConfig, shell.AuditOutcomeOk, time.Since(start).Milliseconds())
    writeShellJSON(w, http.StatusOK, map[string]any{"saved": true, "platform": platform})
}

func (s *Server) handleShellAdapterTest(w http.ResponseWriter, r *http.Request) {
    if s.shellVault == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "adapter vault is not initialized", "", false)
        return
    }
    platform := r.PathValue("platform")
    ok, reason := s.shellVault.Test(platform)
    resp := map[string]any{"ok": ok}
    if reason != "" {
        resp["reason"] = reason
    }
    writeShellJSON(w, http.StatusOK, resp)
}

// --- Connectors -----------------------------------------------------------

func (s *Server) handleShellConnectorsList(w http.ResponseWriter, r *http.Request) {
    if s.shellConnectors == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "connector store is not initialized", "", false)
        return
    }
    writeShellJSON(w, http.StatusOK, map[string]any{"connectors": s.shellConnectors.List()})
}

func (s *Server) handleShellConnectorPut(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    if s.shellConnectors == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "connector store is not initialized", "", false)
        return
    }
    id := r.PathValue("id")
    var doc shell.ConnectorDoc
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShellBody)).Decode(&doc); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode connector: %v", err), id, false)
        return
    }
    doc.ID = id
    problems, err := s.shellConnectors.Put(doc)
    if err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "connector_rejected", err.Error(), id, false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassConfig, shell.AuditOutcomeOk, time.Since(start).Milliseconds())
    writeShellJSON(w, http.StatusOK, map[string]any{"saved": true, "problems": problems})
}

func (s *Server) handleShellConnectorTest(w http.ResponseWriter, r *http.Request) {
    if s.shellConnectors == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "connector store is not initialized", "", false)
        return
    }
    id := r.PathValue("id")
    var req struct {
        Operation string `json:"operation"`
    }
    if r.Body != nil {
        _ = json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShellBody)).Decode(&req)
    }
    testStart := time.Now()
    ok, reason, _ := s.shellConnectors.Test(id, req.Operation)
    resp := map[string]any{
        "ok":         ok,
        "operation":  req.Operation,
        "duration_ms": time.Since(testStart).Milliseconds(),
    }
    if reason != "" {
        resp["reason"] = reason
    }
    writeShellJSON(w, http.StatusOK, resp)
}

// --- Config overrides -----------------------------------------------------

func (s *Server) handleShellConfigGet(w http.ResponseWriter, r *http.Request) {
    if s.shellOverrides == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "config overrides are not initialized", "", false)
        return
    }
    defaults, overrides := s.shellOverrides.Get(r.PathValue("key"))
    writeShellJSON(w, http.StatusOK, map[string]any{"defaults": defaults, "overrides": overrides})
}

func (s *Server) handleShellConfigPut(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    if s.shellOverrides == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "config overrides are not initialized", "", false)
        return
    }
    key := r.PathValue("key")
    raw := json.RawMessage{}
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShellBody)).Decode(&raw); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode override: %v", err), key, false)
        return
    }
    if err := s.shellOverrides.Put(key, raw); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "override_rejected", err.Error(), key, false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassConfig, shell.AuditOutcomeOk, time.Since(start).Milliseconds())
    writeShellJSON(w, http.StatusOK, map[string]any{"saved": true, "key": key})
}

// --- Audit ----------------------------------------------------------------

func (s *Server) handleShellAudit(w http.ResponseWriter, r *http.Request) {
    if s.shellAudit == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "audit log is not initialized", "", false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassRead, shell.AuditOutcomeOk, 0)
    writeShellJSON(w, http.StatusOK, map[string]any{
        "entries": s.shellAudit.Entries(r.URL.Query().Get("user"), r.URL.Query().Get("action_class")),
    })
}

// --- Roles + menu matrix --------------------------------------------------

// shellRoleUserCount counts enabled accounts holding the role (live from the
// user store, so user_count never goes stale).
func (s *Server) shellRoleUserCount(role string) int {
    if s.shellUsers == nil {
        return 0
    }
    n := 0
    for _, a := range s.shellUsers.List() {
        if a.Disabled {
            continue
        }
        if hasAnyRole(a.Roles, role) {
            n++
        }
    }
    return n
}

func (s *Server) handleShellRolesList(w http.ResponseWriter, r *http.Request) {
    if s.shellRoles == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "role store is not initialized", "", false)
        return
    }
    writeShellJSON(w, http.StatusOK, map[string]any{
        "roles":        s.shellRoles.List(s.shellRoleUserCount),
        "write_classes": []string{shell.AuditClassRead, shell.AuditClassWrite, shell.AuditClassApproval, shell.AuditClassAgentRun, shell.AuditClassConfig, shell.AuditClassAuth},
    })
}

func (s *Server) handleShellRoleCreate(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    if s.shellRoles == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "role store is not initialized", "", false)
        return
    }
    var req struct {
        Name         string   `json:"name"`
        Description  string   `json:"description"`
        WriteClasses []string `json:"write_classes"`
    }
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShellBody)).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode role: %v", err), "", false)
        return
    }
    if err := s.shellRoles.Create(req.Name, req.Description, req.WriteClasses); err != nil {
        status := http.StatusBadRequest
        if errors.Is(err, shell.ErrRoleExists) || errors.Is(err, shell.ErrRoleBuiltin) {
            status = http.StatusConflict
        }
        shell.WriteEnvelope(w, status, "role_rejected", err.Error(), req.Name, false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassConfig, shell.AuditOutcomeOk, time.Since(start).Milliseconds())
    writeShellJSON(w, http.StatusOK, map[string]any{"created": true, "name": req.Name})
}

func (s *Server) handleShellRoleDelete(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    if s.shellRoles == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "role store is not initialized", "", false)
        return
    }
    name := r.PathValue("name")
    if err := s.shellRoles.Delete(name, s.shellRoleUserCount); err != nil {
        status := http.StatusBadRequest
        if errors.Is(err, shell.ErrRoleInUse) || errors.Is(err, shell.ErrRoleBuiltin) {
            status = http.StatusConflict
        }
        shell.WriteEnvelope(w, status, "role_rejected", err.Error(), name, false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassConfig, shell.AuditOutcomeOk, time.Since(start).Milliseconds())
    writeShellJSON(w, http.StatusOK, map[string]any{"deleted": true, "name": name})
}

// shellMenuDefaults derives the default role ↔ menu matrix from the live
// manifest registry: every module menu visible to admin + user.
func (s *Server) shellMenuDefaults() []shell.MenuItem {
    if s.shellRegistry == nil {
        return nil
    }
    views := s.shellRegistry.List()
    items := make([]shell.MenuItem, 0, len(views))
    for _, v := range views {
        href := v.Path
        if href == "" {
            href = v.Entry
        }
        items = append(items, shell.MenuItem{
            ID:    v.ID,
            Label: v.Title,
            Href:  href,
            Roles: []string{"admin", "user"},
        })
    }
    return items
}

func (s *Server) handleShellMenuPermissionsGet(w http.ResponseWriter, r *http.Request) {
    if s.shellMenus == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "menu permissions are not initialized", "", false)
        return
    }
    defaults, overrides := s.shellMenus.Get(s.shellMenuDefaults())
    writeShellJSON(w, http.StatusOK, map[string]any{
        "defaults":  map[string]any{"menus": defaults},
        "overrides": map[string]any{"menus": overrides},
    })
}

func (s *Server) handleShellMenuPermissionsPut(w http.ResponseWriter, r *http.Request) {
    start := time.Now()
    if s.shellMenus == nil {
        shell.WriteEnvelope(w, http.StatusServiceUnavailable, "surface_unavailable", "menu permissions are not initialized", "", false)
        return
    }
    var req struct {
        Menus []shell.MenuItem `json:"menus"`
    }
    if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxShellBody)).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode menus: %v", err), "", false)
        return
    }
    roleExists := func(name string) bool { return s.shellRoles != nil && s.shellRoles.Exists(name) }
    if err := s.shellMenus.Put(req.Menus, roleExists); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "menus_rejected", err.Error(), "", false)
        return
    }
    s.shellAuditRecord(r, shell.AuditClassConfig, shell.AuditOutcomeOk, time.Since(start).Milliseconds())
    writeShellJSON(w, http.StatusOK, map[string]any{"saved": true})
}
