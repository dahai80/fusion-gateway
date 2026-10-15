package server

import (
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "path/filepath"
    "strings"

    adminui "github.com/fusion-gateway/fusion-gateway/internal/admin/ui"
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
    s.setupShellUsers(&cfg)
    s.autoRegisterAdminModule()
    slog.Info("shell subsystem initialized",
        "static_manifests", len(cfg.Shell.Manifests),
        "modules", len(cfg.Shell.Modules),
        "users", s.shellUsers.Count(),
        "bootstrap_admin", s.shellBootstrap != nil)
}

// autoRegisterAdminModule registers the built-in admin console (#216, R-5)
// into the manifest registry so the shell host menu always carries it
// without a manual registration step. Metadata comes from the embedded
// dist/manifest.json (single source with the frontend); entry/health are
// composed at runtime from the first entry_origin_allowlist origin because
// the manifest file cannot know the deployment origin. A7 fail-closed: when
// the allowlist is empty or lacks the gateway origin the registration is
// skipped with a WARN — never bypassed.
func (s *Server) autoRegisterAdminModule() {
    m, ok := adminui.Manifest()
    if !ok {
        slog.Warn("shell admin module auto-registration skipped: embedded manifest.json missing or invalid")
        return
    }
    if len(s.cfg.Config.Shell.EntryOriginAllowlist) == 0 {
        slog.Warn("shell admin module auto-registration skipped: entry_origin_allowlist empty")
        return
    }
    origin := s.cfg.Config.Shell.EntryOriginAllowlist[0]
    manifest := config.ShellManifest{
        ID:       m.ID,
        Title:    m.Title,
        TitleEn:  m.TitleEn,
        Version:  m.Version,
        Path:     m.Path,
        Entry:    strings.TrimSuffix(origin, "/") + m.Entry,
        Health:   strings.TrimSuffix(origin, "/") + m.Health,
        Backend:  m.Backend,
        Devices:  m.Devices,
        Category: m.Category,
    }
    if err := s.shellRegistry.Upsert(manifest); err != nil {
        slog.Warn("shell admin module auto-registration rejected",
            "id", manifest.ID, "entry", manifest.Entry, "error", err)
        return
    }
    slog.Info("shell admin module auto-registered",
        "id", manifest.ID, "entry", manifest.Entry, "health", manifest.Health)
}

// setupShellUsers wires the durable RBAC user store (PRD R-1 ③). The store
// file (store.data_dir/shell_users.json) is the source of truth once it
// exists; config users only seed an empty store. The bootstrap admin (no
// configured users) is seeded with must_change_password so the first login
// rotates its random password into the store.
func (s *Server) setupShellUsers(cfg *config.Config) {
    var path string
    if cfg.Store.DataDir != "" {
        // Tilde paths in config.yaml expand here (mirrors EnablePersistence).
        path = filepath.Join(config.ExpandPath(cfg.Store.DataDir), "shell_users.json")
    }
    s.shellUsers = shell.NewUserStore(path)
    if err := s.shellUsers.Load(); err != nil {
        // A corrupt store must not wedge the whole shell subsystem: log
        // loudly and start empty (config re-seeds; existing sessions still
        // validate, admin re-creates accounts).
        slog.Error("shell user store load failed — starting empty", "path", path, "error", err)
        s.shellUsers = shell.NewUserStore(path)
    }
    if len(cfg.Shell.Session.Users) == 0 && s.shellUsers.Count() == 0 {
        // PRD §4.1.5: no configured users -> bootstrap admin with a random
        // password logged once. The plaintext lives only in memory (the log
        // already carries it) so operators can complete the first login.
        username, password := shell.GenerateBootstrapAdmin()
        hash, err := hashShellPassword(password)
        if err != nil {
            slog.Error("shell bootstrap admin hash failed — session login unavailable", "error", err)
            return
        }
        s.shellBootstrap = &shellUserRecord{username: username, password: password, bcryptHash: hash, mustChange: true}
        err = s.shellUsers.Create(shell.UserAccount{
            Username:           username,
            DisplayName:        username,
            PasswordHash:       hash,
            Roles:              []string{"admin"},
            Permissions:        []string{"*"},
            MustChangePassword: true,
        })
        if err != nil {
            slog.Error("shell bootstrap admin store create failed", "error", err)
        }
        return
    }
    accounts := make([]shell.UserAccount, 0, len(cfg.Shell.Session.Users))
    for _, u := range cfg.Shell.Session.Users {
        hash := u.PasswordBcrypt
        if hash == "" && u.Password != "" {
            h, err := hashShellPassword(u.Password)
            if err != nil {
                slog.Error("shell config user hash failed — user skipped", "username", u.Username, "error", err)
                continue
            }
            hash = h
        }
        displayName := u.Username
        roles := u.Roles
        if len(roles) == 0 {
            roles = []string{"user"}
        }
        permissions := u.Modules
        if hasAnyRole(roles, "admin") {
            permissions = []string{"*"}
        }
        accounts = append(accounts, shell.UserAccount{
            Username:     u.Username,
            DisplayName:  displayName,
            PasswordHash: hash,
            Roles:        roles,
            Permissions:  permissions,
        })
    }
    if err := s.shellUsers.SeedFromConfig(accounts); err != nil {
        slog.Error("shell user store seed failed", "error", err)
    }
}

// registerShellRoutes wires the /api/v1/shell/* control plane, the RBAC user
// APIs (R-1 ③), and the /shell/<module_id>/ proxy onto the mux. Auth surfaces:
//   - login: unauthenticated (the login itself is the credential check)
//   - manifest registration + heartbeat: fg-key auth (withMiddleware) — the
//     registering module start.sh carries a gateway API key
//   - everything else (list, me, logout, users, password, proxy): shell
//     session Bearer token; user management additionally requires admin role
func (s *Server) registerShellRoutes(mux *http.ServeMux) {
    mux.HandleFunc("POST /api/v1/shell/session", s.handleShellSessionIssue)
    mux.HandleFunc("POST /api/v1/shell/session/logout", s.withShellSession(s.handleShellSessionLogout))
    mux.HandleFunc("GET /api/v1/shell/me", s.withShellSession(s.handleShellMe))
    mux.HandleFunc("GET /api/v1/shell/manifests", s.withShellSession(s.handleShellManifestList))
    mux.HandleFunc("POST /api/v1/shell/manifests", s.withMiddleware(s.handleShellManifestUpsert))
    mux.HandleFunc("POST /api/v1/shell/manifests/{id}/heartbeat", s.withMiddleware(s.handleShellManifestHeartbeat))
    mux.HandleFunc("GET /api/v1/shell/users", s.withShellSession(requireShellAdmin(s.handleShellUserList)))
    mux.HandleFunc("POST /api/v1/shell/users", s.withShellSession(requireShellAdmin(s.handleShellUserCreate)))
    mux.HandleFunc("PATCH /api/v1/shell/users/{id}", s.withShellSession(requireShellAdmin(s.handleShellUserUpdate)))
    mux.HandleFunc("DELETE /api/v1/shell/users/{id}", s.withShellSession(requireShellAdmin(s.handleShellUserDelete)))
    mux.HandleFunc("POST /api/v1/shell/users/{id}/password-reset", s.withShellSession(requireShellAdmin(s.handleShellUserPasswordReset)))
    mux.HandleFunc("POST /api/v1/password", s.withShellSession(s.handleShellPasswordChange))
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
    displayName string
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
    _ = json.NewEncoder(w).Encode(shellSessionResponse{
        Token:              token,
        TokenType:          "bearer",
        TenantID:           "local",
        MustChangePassword: user.mustChange,
        UserID:             user.username,
        DisplayName:        user.displayName,
        Roles:              user.roles,
        Permissions:        user.modules,
    })
}

// shellSessionResponse is the POST /api/v1/shell/session body (PRD §4.1):
// the bearer token plus the full SessionUser shape the shell host renders.
type shellSessionResponse struct {
    Token              string   `json:"token"`
    TokenType          string   `json:"token_type,omitempty"`
    TenantID           string   `json:"tenant_id"`
    MustChangePassword bool     `json:"must_change_password"`
    UserID             string   `json:"user_id"`
    DisplayName        string   `json:"display_name"`
    Roles              []string `json:"roles"`
    Permissions        []string `json:"permissions"`
}

// resolveShellUser finds the login-able account: the durable user store
// (config-seeded + API-managed) first, then the in-memory bootstrap admin
// record. Disabled accounts never authenticate.
func (s *Server) resolveShellUser(username string) (*shellUserRecord, bool) {
    if s.shellUsers != nil {
        if a, ok := s.shellUsers.Get(username); ok && a.PasswordHash != "" {
            rec := &shellUserRecord{
                username:    a.Username,
                bcryptHash:  a.PasswordHash,
                roles:       a.Roles,
                modules:     a.Permissions,
                mustChange:  a.MustChangePassword,
                displayName: a.DisplayName,
            }
            return rec, !a.Disabled
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

// handleShellMe: GET /api/v1/shell/me — the caller's own identity (PRD §4.1):
// token claims overlaid with the live account record so a password change
// (must_change_password cleared in the store) is reflected without waiting
// for re-login.
func (s *Server) handleShellMe(w http.ResponseWriter, r *http.Request) {
    claims := shellClaimsFrom(r.Context())
    resp := map[string]any{
        "user_id":              claims.Sub,
        "display_name":         claims.Sub,
        "roles":                claims.Roles,
        "permissions":          claims.Modules,
        "tenant_id":            claims.TenantID,
        "must_change_password": claims.MustChangePassword,
    }
    if a, ok := s.shellUsers.Get(claims.Sub); ok {
        // The store is the live state: a completed password change clears
        // must_change_password even for a token minted before the change.
        resp["display_name"] = a.DisplayName
        resp["must_change_password"] = a.MustChangePassword
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(resp)
}

// handleShellManifestList: GET /api/v1/shell/manifests — a bare array of the
// manifest fields + derived status; `unreachable` is the host-menu greying
// flag (stale heartbeat or never-registered static fallback).
func (s *Server) handleShellManifestList(w http.ResponseWriter, r *http.Request) {
    views := s.shellRegistry.List()
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(views)
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
