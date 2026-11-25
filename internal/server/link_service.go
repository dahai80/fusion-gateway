package server

// #717: fusion-link daemon service-ticket plane. The daemon executes
// cloud-dispatched tasks locally when no browser session exists — it needs
// a non-interactive credential for the shell task APIs. Design (PRD
// fusion-link-1125 §7/§11.3, issue #717):
//
//   - POST /api/v1/link/ticket: the daemon exchanges its device credential
//     (web_shell.link.credential / credential_file, constant-time compared)
//     for a short-lived ticket. Loopback-only: non-127.0.0.1 sources are
//     rejected outright.
//   - The ticket is a SEPARATE token space from browser shell sessions
//     (different HMAC key + kind marker; see shell.MintLinkTicket) and is
//     accepted only on the task-execution route allowlist below, only from
//     loopback, never for the login/admin/user planes.
//   - Audit entries carry the task id in the user slot (`link:<task_id>`)
//     so the hash-chained audit log stays schema-stable.
//   - Expiry mid-task surfaces as 401 with envelope code `E_AUTH` — a
//     visible failure, never a silent degrade.

import (
    "crypto/subtle"
    "encoding/json"
    "log/slog"
    "net"
    "net/http"
    "os"
    "strings"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// LinkTicketMaxTTL caps the ticket lifetime (PRD §11.3: <= 15m).
const LinkTicketMaxTTL = 15 * time.Minute

// linkTicketRoutes is the route allowlist a service ticket may drive: the
// task-execution surface (chat completion, model/policy/manifest reads).
// The login plane, user administration, manifest writes and heartbeats are
// explicitly NOT reachable with a service ticket.
var linkTicketRoutes = map[string]bool{
    "POST /api/v1/shell/chat/completions": true,
    "GET /api/v1/shell/me":                true,
    "GET /api/v1/shell/models":            true,
    "GET /api/v1/shell/manifests":         true,
    "GET /api/v1/shell/model-policy":      true,
    "GET /api/v1/model-policy":            true,
}

// isLoopbackRequest reports whether the request source is the loopback
// interface. RemoteAddr is trusted here because the gateway binds localhost
// in the fusion-link deployment posture (PRD §1: gateway keeps localhost
// binding; remote management goes through the mesh, never direct exposure).
func isLoopbackRequest(r *http.Request) bool {
    host, _, err := net.SplitHostPort(r.RemoteAddr)
    if err != nil {
        host = r.RemoteAddr
    }
    ip := net.ParseIP(host)
    return ip != nil && ip.IsLoopback()
}

// linkCredentialLive resolves the device credential against the CURRENT
// config snapshot (hot-reload parity with captchaStoreLive): empty when the
// plane is disabled by the running config, so an off→on reload arms the
// plane without a restart.
func (s *Server) linkCredentialLive() string {
    s.cfgMu.RLock()
    defer s.cfgMu.RUnlock()
    link := s.cfg.Config.Shell.Link
    if !link.Enabled {
        return ""
    }
    if link.Credential != "" {
        return link.Credential
    }
    return s.linkCredentialFile
}

// handleLinkTicketIssue: POST /api/v1/link/ticket {task_id}. Device
// credential -> short-lived loopback service ticket.
func (s *Server) handleLinkTicketIssue(w http.ResponseWriter, r *http.Request) {
    if s.shellSessions == nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "shell_disabled", "shell subsystem is not enabled", "", false)
        return
    }
    if !isLoopbackRequest(r) {
        slog.Warn("link ticket refused: non-loopback source", "remote", r.RemoteAddr)
        if s.shellAudit != nil {
            _ = s.shellAudit.Record("link", "", shell.AuditClassAuth, http.MethodPost,
                "/api/v1/link/ticket", shell.AuditOutcomeDenied, 0)
        }
        shell.WriteEnvelope(w, http.StatusForbidden, "loopback_only",
            "link tickets are issued to loopback callers only", "", false)
        return
    }
    cred := s.linkCredentialLive()
    if cred == "" {
        shell.WriteEnvelope(w, http.StatusNotFound, "link_disabled",
            "link service-ticket plane is not enabled", "", false)
        return
    }
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        TaskID     string `json:"task_id"`
        Credential string `json:"credential"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request",
            "body must be JSON with credential and task_id", "", false)
        return
    }
    if subtle.ConstantTimeCompare([]byte(req.Credential), []byte(cred)) != 1 {
        slog.Warn("link ticket refused: credential mismatch", "remote", r.RemoteAddr)
        if s.shellAudit != nil {
            _ = s.shellAudit.Record("link", "", shell.AuditClassAuth, http.MethodPost,
                "/api/v1/link/ticket", shell.AuditOutcomeDenied, 0)
        }
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_credential",
            "device credential rejected", "", false)
        return
    }
    s.cfgMu.RLock()
    link := s.cfg.Config.Shell.Link
    s.cfgMu.RUnlock()
    ttl := link.TicketTTL
    if ttl <= 0 || ttl > LinkTicketMaxTTL {
        ttl = LinkTicketMaxTTL
    }
    sub := strings.TrimSpace(link.Sub)
    if sub == "" {
        sub = "fusion-link"
    }
    tenant := strings.TrimSpace(link.Tenant)
    if tenant == "" {
        tenant = s.cfg.Config.Shell.Session.Tenant
    }
    if tenant == "" {
        tenant = "local"
    }
    modules := link.Modules
    if len(modules) == 0 {
        modules = []string{"*"}
    }
    taskID := strings.TrimSpace(req.TaskID)
    ticket, err := shell.MintLinkTicket(s.shellSessionSecret, shell.LinkTicketClaims{
        Kind:    shell.LinkTicketKind,
        Sub:     sub,
        Tenant:  tenant,
        TaskID:  taskID,
        Modules: modules,
        Exp:     time.Now().Add(ttl).Unix(),
    })
    if err != nil {
        slog.Error("link ticket mint failed", "task_id", taskID, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "ticket_issue_failed",
            "failed to issue link ticket", "", true)
        return
    }
    // Task id rides the audit user slot (hash-chain schema stays stable).
    if s.shellAudit != nil {
        _ = s.shellAudit.Record("link:"+taskID, sub, shell.AuditClassAuth, http.MethodPost,
            "/api/v1/link/ticket", shell.AuditOutcomeOk, 0)
    }
    slog.Info("link ticket issued", "task_id", taskID, "sub", sub, "ttl", ttl.String())
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]interface{}{
        "ticket":     ticket,
        "token_type": "bearer",
        "expires_in": int(ttl.Seconds()),
        "task_id":    taskID,
    })
}

// tryLinkTicket extends withShellSession with the service-ticket space:
// a Bearer that fails session validation is tried as a link ticket. Returns
// true when the request was HANDLED (valid ticket served, or a ticket-shaped
// token rejected with a contract response) so the caller stops; false means
// "not a link ticket, keep the session error". A valid ticket requires
// loopback source AND a route on the task-execution allowlist; expiry
// surfaces as E_AUTH (visible failure, #717 acceptance).
func (s *Server) tryLinkTicket(w http.ResponseWriter, r *http.Request, token string, next http.HandlerFunc) bool {
    if s.shellSessions == nil || s.linkCredentialLive() == "" {
        return false
    }
    claims, err := shell.ValidateLinkTicket(s.shellSessionSecret, token)
    if err != nil {
        // Expired ticket must fail VISIBLY with the contract error code —
        // the daemon maps it to E_AUTH task failure, not a silent retry.
        if strings.Contains(err.Error(), "expired") {
            slog.Info("link ticket expired mid-call", "task_id", claims.TaskID, "path", r.URL.Path)
            shell.WriteEnvelope(w, http.StatusUnauthorized, "E_AUTH",
                "link service ticket expired", "", false)
            return true
        }
        return false
    }
    if !isLoopbackRequest(r) {
        slog.Warn("link ticket refused: non-loopback use", "remote", r.RemoteAddr, "path", r.URL.Path)
        shell.WriteEnvelope(w, http.StatusForbidden, "loopback_only",
            "link tickets are valid from loopback callers only", "", false)
        return true
    }
    routeKey := r.Method + " " + r.URL.Path
    if !linkTicketRoutes[routeKey] {
        slog.Warn("link ticket refused: route not in task allowlist", "method", r.Method, "path", r.URL.Path)
        shell.WriteEnvelope(w, http.StatusForbidden, "route_not_allowed",
            "link tickets are scoped to shell task APIs", "", false)
        return true
    }
    session := linkClaimsToSession(claims)
    slog.Info("link ticket accepted", "task_id", claims.TaskID, "path", r.URL.Path)
    if s.shellAudit != nil {
        _ = s.shellAudit.Record("link:"+claims.TaskID, claims.Sub, shell.AuditClassRead,
            r.Method, r.URL.Path, shell.AuditOutcomeOk, 0)
    }
    next(w, r.WithContext(withShellToken(withShellClaims(r.Context(), session), token)))
    return true
}

// linkClaimsToSession maps validated ticket claims into the shell session
// claim shape the task routes already understand. Role "viewer" — the
// ticket plane is execution-scoped, never an admin elevation.
func linkClaimsToSession(claims shell.LinkTicketClaims) shell.SessionClaims {
    return shell.SessionClaims{
        Sub:      claims.Sub + "/" + claims.TaskID,
        Roles:    []string{"viewer"},
        Modules:  claims.Modules,
        TenantID: claims.Tenant,
        Origin:   "link",
    }
}

// expandLinkCredentialFile reads the credential_file (tilde-expanded) once
// at setup; missing/unreadable file disables the plane (fail-closed, #370
// convention).
func expandLinkCredentialFile(cfg config.ShellLinkConfig) string {
    if cfg.CredentialFile == "" || cfg.Credential != "" {
        return ""
    }
    raw, err := os.ReadFile(config.ExpandPath(cfg.CredentialFile))
    if err != nil {
        slog.Error("link credential_file unreadable — link plane disabled", "path", cfg.CredentialFile, "error", err)
        return ""
    }
    cred := strings.TrimSpace(string(raw))
    if len(cred) < 32 {
        slog.Error("link credential_file too short — link plane disabled", "path", cfg.CredentialFile)
        return ""
    }
    return cred
}
