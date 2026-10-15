package server

import (
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
    "golang.org/x/crypto/bcrypt"
)

// buildShellSessionSecret derives the session signing secret: the explicit
// shell.session.secret wins; otherwise a domain-separated digest of the
// master_key (never the raw master key itself). Config validate guarantees
// at least one source when shell.enabled.
func buildShellSessionSecret(cfg *config.Config) []byte {
    if s := strings.TrimSpace(cfg.Shell.Session.Secret); s != "" {
        return []byte(s)
    }
    sum := sha256.Sum256([]byte(cfg.Auth.MasterKey + ":shell-session-v1"))
    return []byte(hex.EncodeToString(sum[:]))
}

// setupShell constructs the shell subsystem (#215) when shell.enabled. A
// disabled subsystem leaves everything nil: no routes, no poll worker, no
// registry (mirrors the browser/identity opt-in gates).
func (s *Server) setupShell() {
    cfg := s.cfg.Config
    if !cfg.Shell.Enabled {
        return
    }
    // Reset any prior construction (tests / re-setup) before rebuilding so a
    // configured-user setup never inherits a stale bootstrap admin.
    s.shellBootstrap = nil
    secret := buildShellSessionSecret(&cfg)
    if cfg.Shell.Session.Secret == "" {
        slog.Info("shell session secret derived from master_key (domain-separated digest)")
    }
    s.shellSessions = shell.NewManager(secret, cfg.Shell.Session.TTL)
    s.shellRegistry = shell.NewRegistry(cfg.Shell.Manifests, cfg.Shell.EntryOriginAllowlist, cfg.Shell.HeartbeatTimeout)
    s.shellProxy = shell.NewProxy(func(moduleID string) string {
        if m, ok := s.cfg.Config.Shell.Modules[moduleID]; ok {
            return m.Upstream
        }
        return ""
    })
    // PRD §4.1.5: no configured users -> bootstrap admin with a random
    // password logged once. The hash lives only in memory.
    if len(cfg.Shell.Session.Users) == 0 {
        username, password := shell.GenerateBootstrapAdmin()
        hash, err := hashShellPassword(password)
        if err != nil {
            slog.Error("shell bootstrap admin hash failed — session login unavailable", "error", err)
        } else {
            // The plaintext lives only in memory (the log already carries it
            // per PRD §4.1.5) so tests/operators can complete the first login
            // before the forced password change.
            s.shellBootstrap = &shellUserRecord{username: username, password: password, bcryptHash: hash, mustChange: true}
        }
    }
    slog.Info("shell subsystem initialized",
        "static_manifests", len(cfg.Shell.Manifests),
        "modules", len(cfg.Shell.Modules),
        "bootstrap_admin", s.shellBootstrap != nil)
}

// registerShellRoutes wires the /api/v1/shell/* control plane and the
// /shell/<module_id>/ proxy onto the mux. Auth surfaces:
//   - login: unauthenticated (the login itself is the credential check)
//   - manifest registration + heartbeat: fg-key auth (withMiddleware) — the
//     registering module start.sh carries a gateway API key
//   - everything else (list, me, logout, proxy): shell session Bearer token
func (s *Server) registerShellRoutes(mux *http.ServeMux) {
    mux.HandleFunc("POST /api/v1/shell/session", s.handleShellSessionIssue)
    mux.HandleFunc("POST /api/v1/shell/session/logout", s.withShellSession(s.handleShellSessionLogout))
    mux.HandleFunc("GET /api/v1/shell/me", s.withShellSession(s.handleShellMe))
    mux.HandleFunc("GET /api/v1/shell/manifests", s.withShellSession(s.handleShellManifestList))
    mux.HandleFunc("POST /api/v1/shell/manifests", s.withMiddleware(s.handleShellManifestUpsert))
    mux.HandleFunc("POST /api/v1/shell/manifests/{id}/heartbeat", s.withMiddleware(s.handleShellManifestHeartbeat))
    mux.HandleFunc("/shell/", s.withShellSession(s.shellProxy.ServeHTTP))
    slog.Info("shell routes registered",
        "control_plane", "/api/v1/shell/*",
        "proxy", "/shell/{module_id}/")
}

// shellUserRecord is one login-able shell account (config or bootstrap).
type shellUserRecord struct {
    username    string
    password    string // plaintext (config WARN path)
    bcryptHash  string
    roles       []string
    modules     []string
    mustChange  bool
}

const maxShellBody = 1 << 20 // 1 MiB

func hashShellPassword(password string) (string, error) {
    hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
    if err != nil {
        return "", err
    }
    return string(hash), nil
}

// withShellSession guards a handler behind a valid shell session Bearer
// token. 401 with the standardized envelope — the shell host intercepts 401
// and bounces to the login page (PRD §4.1.4).
func (s *Server) withShellSession(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if s.shellSessions == nil {
            shell.WriteEnvelope(w, http.StatusNotFound, "shell_disabled", "shell subsystem is not enabled", "", false)
            return
        }
        token := bearerToken(r)
        if token == "" {
            w.Header().Set("WWW-Authenticate", `Bearer realm="fusion-shell"`)
            shell.WriteEnvelope(w, http.StatusUnauthorized, "session_required", "shell session token required", "", false)
            return
        }
        claims, err := s.shellSessions.Validate(token)
        if err != nil {
            slog.Info("shell session rejected", "path", r.URL.Path, "error", err)
            w.Header().Set("WWW-Authenticate", `Bearer realm="fusion-shell", error="invalid_token"`)
            shell.WriteEnvelope(w, http.StatusUnauthorized, "session_invalid", err.Error(), "", false)
            return
        }
        next(w, r.WithContext(withShellClaims(r.Context(), claims)))
    }
}

func bearerToken(r *http.Request) string {
    h := r.Header.Get("Authorization")
    if t, ok := strings.CutPrefix(h, "Bearer "); ok {
        return strings.TrimSpace(t)
    }
    return ""
}

// handleShellSessionIssue: POST /api/v1/shell/session {username,password}.
// Constant-time credential check against config users (bcrypt preferred) or
// the in-memory bootstrap admin. Issues a signed session token.
func (s *Server) handleShellSessionIssue(w http.ResponseWriter, r *http.Request) {
    if s.shellSessions == nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "shell_disabled", "shell subsystem is not enabled", "", false)
        return
    }
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        Username string `json:"username"`
        Password string `json:"password"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil || req.Username == "" {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be JSON with username and password", "", false)
        return
    }

    user, ok := s.resolveShellUser(req.Username)
    if !ok || !checkShellPassword(user, req.Password) {
        slog.Warn("shell session login failed", "username", req.Username, "remote", r.RemoteAddr)
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password", "", false)
        return
    }

    token, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub:                user.username,
        Roles:              user.roles,
        Modules:            user.modules,
        TenantID:           "local",
        MustChangePassword: user.mustChange,
    })
    if err != nil {
        slog.Error("shell session issue failed", "username", user.username, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "session_issue_failed", "failed to issue session token", "", true)
        return
    }
    slog.Info("shell session issued", "username", user.username, "roles", user.roles)
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "token":                 token,
        "token_type":            "bearer",
        "tenant_id":             "local",
        "must_change_password":  user.mustChange,
    })
}

// resolveShellUser finds the account by username: config users first, then
// the bootstrap admin. Roles/modules resolve to full access for role=admin.
func (s *Server) resolveShellUser(username string) (*shellUserRecord, bool) {
    cfg := s.cfg.Config
    for _, u := range cfg.Shell.Session.Users {
        if u.Username == username {
            rec := &shellUserRecord{
                username:   u.Username,
                password:   u.Password,
                bcryptHash: u.PasswordBcrypt,
                roles:      u.Roles,
                modules:    u.Modules,
            }
            for _, role := range u.Roles {
                if role == "admin" {
                    rec.modules = []string{"*"}
                }
            }
            return rec, true
        }
    }
    if s.shellBootstrap != nil && s.shellBootstrap.username == username {
        return s.shellBootstrap, true
    }
    return nil, false
}

func checkShellPassword(user *shellUserRecord, password string) bool {
    if user.bcryptHash != "" {
        return bcrypt.CompareHashAndPassword([]byte(user.bcryptHash), []byte(password)) == nil
    }
    if user.password != "" {
        return subtle.ConstantTimeCompare([]byte(user.password), []byte(password)) == 1
    }
    return false
}

// handleShellSessionLogout: POST /api/v1/shell/session/logout. Revokes the
// presented token (denylist until natural expiry).
func (s *Server) handleShellSessionLogout(w http.ResponseWriter, r *http.Request) {
    if token := bearerToken(r); token != "" {
        s.shellSessions.Revoke(token)
    }
    slog.Info("shell session logged out")
    shell.WriteEnvelope(w, http.StatusOK, "ok", "logged out", "", false)
}

// handleShellMe: GET /api/v1/shell/me — the caller's own claims so the shell
// host can filter the menu by permissions[].
func (s *Server) handleShellMe(w http.ResponseWriter, r *http.Request) {
    claims := shellClaimsFrom(r.Context())
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{
        "user_id":              claims.Sub,
        "roles":                claims.Roles,
        "permissions":          claims.Modules,
        "tenant_id":            claims.TenantID,
        "must_change_password": claims.MustChangePassword,
    })
}

// handleShellManifestList: GET /api/v1/shell/manifests — full list with
// derived status (online/unreachable/offline), static fallback included.
func (s *Server) handleShellManifestList(w http.ResponseWriter, r *http.Request) {
    views := s.shellRegistry.List()
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]any{"manifests": views})
}

// handleShellManifestUpsert: POST /api/v1/shell/manifests — module
// self-registration (idempotent by id, PRD §5.2). fg-key auth; entry origin
// whitelist enforced (A7: reject 403 at registration).
func (s *Server) handleShellManifestUpsert(w http.ResponseWriter, r *http.Request) {
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var m config.ShellManifest
    if err := json.NewDecoder(body).Decode(&m); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("decode manifest: %v", err), "", false)
        return
    }
    if err := s.shellRegistry.Upsert(m); err != nil {
        slog.Warn("shell manifest upsert rejected", "id", m.ID, "error", err)
        shell.WriteEnvelope(w, http.StatusForbidden, "manifest_rejected", err.Error(), m.ID, false)
        return
    }
    shell.WriteEnvelope(w, http.StatusOK, "ok", "manifest registered", m.ID, false)
}

// handleShellManifestHeartbeat: POST /api/v1/shell/manifests/{id}/heartbeat
// — keepalive that keeps the registration online.
func (s *Server) handleShellManifestHeartbeat(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    if err := s.shellRegistry.Heartbeat(id); err != nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "manifest_unknown", err.Error(), id, false)
        return
    }
    shell.WriteEnvelope(w, http.StatusOK, "ok", "heartbeat accepted", id, false)
}
