package server

import (
    "bytes"
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "path/filepath"
    "strings"
    "time"

    adminui "github.com/fusion-gateway/fusion-gateway/internal/admin/ui"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/logthrottle"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
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
    if cfg.Store.DataDir != "" {
        // Dynamic registrations survive restarts (#230). Load before the
        // admin auto-register so an upsert collision resolves to the fresh
        // embedded manifest. A corrupt store must not wedge the shell
        // subsystem: log loudly and continue memory-only.
        path := filepath.Join(config.ExpandPath(cfg.Store.DataDir), "shell_manifests.json")
        s.shellRegistry.SetPersistence(path)
        if err := s.shellRegistry.Load(); err != nil {
            slog.Error("shell manifest store load failed — continuing memory-only", "path", path, "error", err)
        }
    }
    s.shellProxy = shell.NewProxy(func(moduleID string) (string, string) {
        // #232: static config wins the UPSTREAM (override); a dynamic
        // manifest carrying its own upstream is the fallback so
        // self-registering modules are proxied without a config edit +
        // restart. Both paths keep the same entry-origin allowlist (checked
        // at registration) and api_key injection semantics.
        //
        // #294: the CREDENTIAL prefers the dynamic registration when it
        // points at the same upstream as the static entry — the fm- key the
        // gateway issued (and the module validates) is kept fresh on the
        // registry entry across rotations, while a hardcoded static api_key
        // goes stale and produces module-side 401 API_KEY_INVALID. When the
        // upstreams differ (#256 engine-vs-bridge split) the static api_key
        // stays — it authenticates the static upstream, not the module's
        // self-registered one.
        dynUp, dynKey, dynOK := s.shellRegistry.UpstreamFor(moduleID)
        if m, ok := s.cfg.Config.Shell.Modules[moduleID]; ok && m.Upstream != "" {
            if dynOK && dynKey != "" && sameShellUpstream(dynUp, m.Upstream) {
                if m.APIKey != "" && m.APIKey != dynKey {
                    if shellCredentialDivergeLog.Allow(moduleID) {
                        slog.Warn("shell proxy: static api_key diverges from dynamic registration credential; dynamic wins (remove the stale static entry or leave it as fallback)",
                            "module", moduleID)
                    }
                }
                return m.Upstream, dynKey
            }
            return m.Upstream, m.APIKey
        }
        if dynOK {
            return dynUp, dynKey
        }
        return "", ""
    })
    // #256: ext endpoints a self-registered manifest declares
    // (searchProviders/notifications) route to that registration's own
    // upstream when static config pins the module elsewhere (engine vs web
    // bridge). Static upstream keeps serving everything else (#232).
    s.shellProxy.SetExtResolver(s.shellRegistry.ExtRouteFor)
    // #261 K1: a revoked service key degrades the proxy immediately (503
    // module_key_revoked) instead of forwarding unauthenticated.
    s.shellProxy.SetRevokedChecker(s.shellRegistry.IsServiceKeyRevoked)
    s.setupShellUsers(&cfg)
    s.setupShellAdminSurfaces(&cfg)
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
// dist/manifest.json (single source with the frontend). Entry/health stay
// RELATIVE paths (/admin/, /healthz): the shell host serves same-origin from
// the gateway (prod) or proxies /admin/ in dev (#223 relative-entry
// convention). An absolute entry composed from entry_origin_allowlist would
// break the session cookie cross-origin — a dev host on :11430 embedding
// http://127.0.0.1:11432 is cross-site (localhost vs 127.0.0.1), so the
// SameSite cookie never attaches and the iframe 401s. Relative entries are
// same-origin by construction (#223) and never need the allowlist — but a
// non-empty allowlist still gates whether shell mode is safe at all, so the
// A7 fail-closed skip on an empty allowlist stays.
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
    manifest := config.ShellManifest{
        ID:       m.ID,
        Title:    m.Title,
        TitleEn:  m.TitleEn,
        Version:  m.Version,
        Path:     m.Path,
        Entry:    m.Entry,
        Health:   m.Health,
        Backend:  m.Backend,
        Devices:  m.Devices,
        Category: m.Category,
    }
    if err := s.shellRegistry.Upsert(manifest); err != nil {
        slog.Warn("shell admin module auto-registration rejected",
            "id", manifest.ID, "entry", manifest.Entry, "error", err)
        return
    }
    // The built-in admin module is gateway-owned: mark it so API deletion
    // (#253) rejects it like a static config entry.
    s.shellRegistry.MarkBuiltin(manifest.ID)
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
    // #267: session-gated chat completions for the shell Home delegation
    // entry — same LLM router as /v1/chat/completions, but the credential
    // is the shell session JWT (the shell must not hold a gateway API key).
    mux.HandleFunc("POST /api/v1/shell/chat/completions", s.withShellSession(s.handleChatCompletions))
    mux.HandleFunc("POST /api/v1/shell/manifests", s.withMiddleware(s.withShellManifestUpsertAuth(s.handleShellManifestUpsert)))
    // #261 K1: heartbeat authenticates with the module's gateway-issued fm-
    // service key (hash-compare against the registry), NOT the standard fg-
    // key chain — an fm- Bearer would be 401'd by APIKeyAuthWithStore before
    // reaching the handler. Master/admin-key heartbeats keep working for
    // back-compat, so the wrapper parses the credential itself and the route
    // stays outside withMiddleware.
    mux.HandleFunc("POST /api/v1/shell/manifests/{id}/heartbeat", s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat))
    mux.HandleFunc("POST /api/v1/shell/manifests/{id}/key/rotate", s.withMiddleware(s.withShellManifestAuth(s.handleShellServiceKeyRotate)))
    mux.HandleFunc("DELETE /api/v1/shell/manifests/{id}/key", s.withMiddleware(s.withShellManifestAuth(s.handleShellServiceKeyRevoke)))
    mux.HandleFunc("DELETE /api/v1/shell/manifests/{id}", s.withMiddleware(s.withShellManifestAuth(s.handleShellManifestDelete)))
    mux.HandleFunc("GET /api/v1/shell/users", s.withShellSession(requireShellAdmin(s.handleShellUserList)))
    mux.HandleFunc("POST /api/v1/shell/users", s.withShellSession(requireShellAdmin(s.handleShellUserCreate)))
    mux.HandleFunc("PATCH /api/v1/shell/users/{id}", s.withShellSession(requireShellAdmin(s.handleShellUserUpdate)))
    mux.HandleFunc("DELETE /api/v1/shell/users/{id}", s.withShellSession(requireShellAdmin(s.handleShellUserDelete)))
    mux.HandleFunc("POST /api/v1/shell/users/{id}/password-reset", s.withShellSession(requireShellAdmin(s.handleShellUserPasswordReset)))
    mux.HandleFunc("POST /api/v1/password", s.withShellSession(s.handleShellPasswordChange))
    // #271: session-gated surface for the shell Settings → API Keys page.
    // The master key can never reach a browser, so admin-role shell users
    // manage K2 user keys through their session; the handlers are the K2
    // ones unchanged (dahai80/fusion-web-shell#35).
    mux.HandleFunc("GET /api/v1/shell/keys", s.withShellSession(requireShellAdmin(s.handleUserKeysList)))
    mux.HandleFunc("POST /api/v1/shell/keys", s.withShellSession(requireShellAdmin(s.handleUserKeyCreate)))
    mux.HandleFunc("POST /api/v1/shell/keys/{name}/rotate", s.withShellSession(requireShellAdmin(s.handleUserKeyRotate)))
    mux.HandleFunc("DELETE /api/v1/shell/keys/{name}", s.withShellSession(requireShellAdmin(s.handleUserKeyRevoke)))
    // #275: E6 admin surfaces (adapters vault / connectors / config
    // overrides / audit / roles + menu matrix).
    s.registerShellAdminRoutes(mux)
    mux.HandleFunc("/shell/", s.withShellSession(s.withShellModuleAccess(s.shellProxy.ServeHTTP)))
    slog.Info("shell routes registered",
        "control_plane", "/api/v1/shell/*",
        "proxy", "/shell/{module_id}/")
}

// withShellModuleAccess enforces the module-level permission check in the
// proxy layer (PRD §4.2, acceptance F-2 / S-8): menu visibility is only the
// first check — the /shell/<module>/ API validates the session's module
// permissions again (the "*" wildcard expands to everything). Unauthorized
// access returns 403 with the standard error envelope, NOT an upstream dial.
// Must run inside withShellSession (reads the session claims from context).
func (s *Server) withShellModuleAccess(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        claims := shellClaimsFrom(r.Context())
        moduleID, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/shell/"), "/")
        if !shellModuleAllowed(claims.Modules, moduleID) {
            slog.Warn("shell proxy denied: module not in session permissions",
                "module", moduleID, "user", claims.Sub, "permissions", claims.Modules)
            shell.WriteEnvelope(w, http.StatusForbidden, "module_forbidden",
                fmt.Sprintf("module %q is not in your permissions", moduleID), moduleID, false)
            return
        }
        next(w, r)
    }
}

// shellModuleAllowed expands the permission list against one module id.
func shellModuleAllowed(permissions []string, moduleID string) bool {
    for _, p := range permissions {
        if p == "*" || p == moduleID {
            return true
        }
    }
    return false
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

// shellSessionCookieName is the HttpOnly session cookie (#220) set at
// issuance and cleared at logout, so iframe navigation requests (which
// cannot attach an Authorization header) authenticate on /shell/*.
const shellSessionCookieName = "fusion_shell_session"

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
            // #220: iframe navigation requests cannot attach an Authorization
            // header. The session issuance also sets an HttpOnly cookie, so
            // the browser carries the session on iframe loads automatically.
            if c, err := r.Cookie(shellSessionCookieName); err == nil {
                token = c.Value
            }
        }
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
        if s.shellAudit != nil {
            _ = s.shellAudit.Record(req.Username, "", shell.AuditClassAuth, http.MethodPost,
                "/api/v1/shell/session", shell.AuditOutcomeDenied, 0)
        }
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
    if s.shellAudit != nil {
        role := ""
        if len(user.roles) > 0 {
            role = user.roles[0]
        }
        _ = s.shellAudit.Record(user.username, role, shell.AuditClassAuth, http.MethodPost,
            "/api/v1/shell/session", shell.AuditOutcomeOk, 0)
    }
    // #220: also set an HttpOnly SameSite=Strict session cookie so iframe
    // navigation requests (no Authorization header possible) authenticate on
    // /shell/*. Secure flag mirrors the transport (TLS deployment = Secure).
    http.SetCookie(w, &http.Cookie{
        Name:     shellSessionCookieName,
        Value:    token,
        Path:     "/",
        MaxAge:   int(s.shellSessions.TTL().Seconds()),
        HttpOnly: true,
        Secure:   r.TLS != nil,
        SameSite: http.SameSiteStrictMode,
    })
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
    if c, err := r.Cookie(shellSessionCookieName); err == nil && c.Value != "" {
        s.shellSessions.Revoke(c.Value)
    }
    http.SetCookie(w, &http.Cookie{
        Name:     shellSessionCookieName,
        Value:    "",
        Path:     "",
        MaxAge:   -1,
        HttpOnly: true,
        Secure:   r.TLS != nil,
        SameSite: http.SameSiteStrictMode,
    })
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
// flag (stale heartbeat or never-registered static fallback). Filtered to
// the session's permission subset (#229, PRD §4 menu visibility): a limited
// session must not be able to enumerate module ids/titles/health URLs it
// cannot access. `*` sees all; an empty permission list sees nothing
// (fail-closed, same gate as the proxy RBAC).
func (s *Server) handleShellManifestList(w http.ResponseWriter, r *http.Request) {
    claims := shellClaimsFrom(r.Context())
    views := s.shellRegistry.List()
    filtered := make([]shell.ManifestView, 0, len(views))
    for _, v := range views {
        if shellModuleAllowed(claims.Modules, v.ID) {
            filtered = append(filtered, v)
        }
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(filtered)
}

// withShellManifestAuth (#243): manifest writes are an admin-plane
// operation — require the master key or the designated web_shell.admin_key
// (env FUSION_GATEWAY_ADMIN_KEY, the credential downstream modules
// document). Plain client keys are rejected: letting any consumer key
// register or heartbeat manifests was a privilege inversion, and the
// fg-admin-key value modules historically passed is a backend stub the
// auth chain never accepted anyway (the "dead-on-arrival" registration).
// Runs INSIDE withMiddleware so the principal is already resolved.
// shellManifestAdminAllowed reports whether the request carries the master
// key or the web_shell.admin_key (constant-time) — the admin plane shared by
// manifest upsert/rotate/revoke/delete.
func (s *Server) shellManifestAdminAllowed(r *http.Request) bool {
    adminKey := ""
    if s.cfg != nil {
        adminKey = s.cfg.Config.Shell.AdminKey
    }
    p := middleware.PrincipalFromContext(r.Context())
    allowed := p != nil && p.IsMaster
    if !allowed && adminKey != "" && p != nil && p.KeyConfig != nil {
        allowed = subtle.ConstantTimeCompare([]byte(p.KeyConfig.Key), []byte(adminKey)) == 1
    }
    return allowed
}

// withShellManifestUpsertAuth (#369): the upsert gate. Admin plane as
// before, PLUS a module self-heal path — a module may re-register ITS OWN id
// authenticated by its still-valid gateway-issued fm- service key, so the
// standard recovery loop after a heartbeat 403/404 is "re-POST the manifest
// with the fm- key", never "ship the admin key to every module process".
// The body is buffered to extract the id for the key check, then re-armed.
func (s *Server) withShellManifestUpsertAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if s.shellManifestAdminAllowed(r) {
            next(w, r)
            return
        }
        body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxShellBody))
        if err != nil {
            shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "failed to read manifest body", "", false)
            return
        }
        var probe struct {
            ID string `json:"id"`
        }
        token := shellBearerToken(r)
        if json.Unmarshal(body, &probe) == nil && probe.ID != "" &&
            shell.IsServiceKeyPlaintext(token) && s.shellRegistry != nil {
            sum := sha256.Sum256([]byte(token))
            grace := s.cfg.Config.Shell.KeyRotationGrace
            if grace <= 0 {
                grace = 24 * time.Hour
            }
            if s.shellRegistry.AuthServiceKey(probe.ID, hex.EncodeToString(sum[:]), grace) {
                slog.Info("shell manifest re-register authenticated by module service key (#369 self-heal)", "id", probe.ID)
                r.Body = io.NopCloser(bytes.NewReader(body))
                r.ContentLength = int64(len(body))
                next(w, r)
                return
            }
        }
        keyName := "anonymous"
        if p := middleware.PrincipalFromContext(r.Context()); p != nil && p.KeyConfig != nil && p.KeyConfig.Name != "" {
            keyName = p.KeyConfig.Name
        }
        slog.Warn("shell manifest write denied: requires admin key or the module's own valid service key",
            "path", r.URL.Path, "key", keyName)
        shell.WriteEnvelope(w, http.StatusForbidden, "manifest_forbidden",
            "manifest registration requires the master key, web_shell.admin_key, or the module's own valid fm- service key (same id)", "", false)
    }
}

func (s *Server) withShellManifestAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        allowed := s.shellManifestAdminAllowed(r)
        if !allowed {
            keyName := "anonymous"
            if p := middleware.PrincipalFromContext(r.Context()); p != nil && p.KeyConfig != nil && p.KeyConfig.Name != "" {
                keyName = p.KeyConfig.Name
            }
            slog.Warn("shell manifest write denied: requires master key or web_shell.admin_key",
                "path", r.URL.Path, "key", keyName)
            shell.WriteEnvelope(w, http.StatusForbidden, "manifest_forbidden",
                "manifest registration requires the master key or web_shell.admin_key", "", false)
            return
        }
        next(w, r)
    }
}

// handleShellManifestUpsert: POST /api/v1/shell/manifests — module
// self-registration (idempotent by id, PRD §5.2). fg-key auth + admin-plane
// gate (#243); entry origin whitelist enforced (A7: reject 403 at registration).
// #261 K1: when the body omits api_key, the gateway issues an fm- service
// key for the module and returns the plaintext exactly once. A body that
// carries api_key keeps the historical module-supplied credential path.
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
    if m.APIKey != "" {
        shell.WriteEnvelope(w, http.StatusOK, "ok", "manifest registered", m.ID, false)
        return
    }
    keyID, plaintext, issued, err := s.shellRegistry.EnsureServiceKey(m.ID)
    if err != nil {
        slog.Error("shell service key issuance failed after upsert", "id", m.ID, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "service_key_error", err.Error(), m.ID, false)
        return
    }
    if !issued {
        // Idempotent re-registration: the existing key is retained and the
        // plaintext is NOT re-revealed (returned exactly once, at issuance).
        // A module that lost its key asks an admin to rotate.
        writeShellKeyResponse(w, http.StatusOK, "ok", "manifest registered (service key retained; raw key shown only at issuance)", m.ID, keyID, "")
        return
    }
    writeShellKeyResponse(w, http.StatusOK, "ok", "manifest registered; store the service key now — it is shown exactly once", m.ID, keyID, plaintext)
}

// writeShellKeyResponse emits the issuance/rotation payload: the standard
// envelope fields plus the non-secret key_id and the raw_key plaintext that
// is returned exactly once (#261 K1).
func writeShellKeyResponse(w http.ResponseWriter, status int, code, message, moduleID, keyID, rawKey string) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(struct {
        Code     string `json:"code"`
        Message  string `json:"message"`
        ModuleID string `json:"module_id,omitempty"`
        KeyID    string `json:"key_id,omitempty"`
        RawKey   string `json:"raw_key,omitempty"`
    }{Code: code, Message: message, ModuleID: moduleID, KeyID: keyID, RawKey: rawKey})
}

// heartbeatDenyLog throttles heartbeat-denial WARNs to 1 per 5m per
// (id, reason) (#281): a module whose credential is permanently wrong used to
// log one WARN every 15s forever (1944/day), burying real errors. Every
// denial still increments fusion_gateway_shell_heartbeat_denied_total, so the
// condition stays observable in /metrics while the log stays quiet.
var heartbeatDenyLog = logthrottle.New(5 * time.Minute)

// shellCredentialDivergeLog throttles the #294 divergence WARN (1 per 5m per
// module): a stale static api_key next to a live dynamic registration is a
// permanent condition until the operator cleans the config — an unthrottled
// log would flood at request rate.
var shellCredentialDivergeLog = logthrottle.New(5 * time.Minute)

// sameShellUpstream compares two module upstream URLs ignoring a trailing
// slash so the #294 dynamic-credential preference is not defeated by
// cosmetic config differences.
func sameShellUpstream(a, b string) bool {
    return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}

// withShellHeartbeatAuth gates the heartbeat endpoint (#261 K1). Accepted
// credentials, in order: master key, web_shell.admin_key (both constant-time
// — back-compat for admin-plane scripts), or the module's gateway-issued
// fm- service key (SHA-256 hash compare against the registry entry bound to
// the {id} in the path). Deliberately outside withMiddleware: the standard
// fg-key chain would 401 an fm- Bearer before this gate could evaluate it.
func (s *Server) withShellHeartbeatAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        id := r.PathValue("id")
        token := shellBearerToken(r)
        if token == "" {
            if heartbeatDenyLog.Allow(id + "|missing") {
                slog.Warn("shell heartbeat denied: missing credential (throttled to 1 per 5m)", "id", id)
            }
            observability.RecordShellHeartbeatDenied(id, "missing")
            shell.WriteEnvelope(w, http.StatusUnauthorized, "heartbeat_unauthorized", "missing Authorization Bearer credential", id, false)
            return
        }
        if s.shellHeartbeatAdminToken(token) {
            next(w, r)
            return
        }
        if shell.IsServiceKeyPlaintext(token) {
            sum := sha256.Sum256([]byte(token))
            grace := s.cfg.Config.Shell.KeyRotationGrace
            if grace <= 0 {
                grace = 24 * time.Hour
            }
            if s.shellRegistry.AuthServiceKey(id, hex.EncodeToString(sum[:]), grace) {
                next(w, r)
                return
            }
            // #369: a denied fm- key is three different conditions with
            // three different recovery paths. Give the module a programmable
            // signal instead of one ambiguous 403:
            //   404 manifest_unknown     — registration gone (gateway
            //                             restarted without store.data_dir,
            //                             or the entry was removed);
            //                             re-register via POST /manifests
            //   403 heartbeat_key_revoked — key revoked by an admin; ask an
            //                             admin to rotate, self-heal cannot
            //                             recover a revoked key
            //   403 heartbeat_forbidden   — key mismatch for a live
            //                             registration; fix the credential
            if !s.shellRegistry.DynamicRegistered(id) {
                if heartbeatDenyLog.Allow(id + "|unknown") {
                    slog.Warn("shell heartbeat denied: registration not found (throttled to 1 per 5m)", "id", id)
                }
                observability.RecordShellHeartbeatDenied(id, "unknown")
                shell.WriteEnvelope(w, http.StatusNotFound, "manifest_unknown",
                    "no registration for this module id; re-register via POST /api/v1/shell/manifests (admin key, or the module service key while one is still valid)", id, false)
                return
            }
            if s.shellRegistry.IsServiceKeyRevoked(id) {
                if heartbeatDenyLog.Allow(id + "|revoked") {
                    slog.Warn("shell heartbeat denied: service key revoked (throttled to 1 per 5m)", "id", id)
                }
                observability.RecordShellHeartbeatDenied(id, "revoked")
                shell.WriteEnvelope(w, http.StatusForbidden, "heartbeat_key_revoked",
                    "module service key was revoked; an admin must rotate it, then the module re-registers", id, false)
                return
            }
        }
        if heartbeatDenyLog.Allow(id + "|invalid") {
            slog.Warn("shell heartbeat denied: invalid credential (throttled to 1 per 5m)", "id", id)
        }
        observability.RecordShellHeartbeatDenied(id, "invalid")
        shell.WriteEnvelope(w, http.StatusForbidden, "heartbeat_forbidden", "heartbeat requires the module service key, web_shell.admin_key, or the master key", id, false)
    }
}

// shellHeartbeatAdminToken reports whether the presented credential is the
// master key or the web_shell.admin_key (constant-time; either may be unset).
func (s *Server) shellHeartbeatAdminToken(token string) bool {
    if s.cfg == nil {
        return false
    }
    if mk := s.cfg.Config.Auth.MasterKey; mk != "" && subtle.ConstantTimeCompare([]byte(token), []byte(mk)) == 1 {
        return true
    }
    if ak := s.cfg.Config.Shell.AdminKey; ak != "" && subtle.ConstantTimeCompare([]byte(token), []byte(ak)) == 1 {
        return true
    }
    return false
}

// shellBearerToken extracts the Authorization Bearer credential.
func shellBearerToken(r *http.Request) string {
    auth := r.Header.Get("Authorization")
    if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
        return strings.TrimSpace(auth[7:])
    }
    return ""
}

// handleShellServiceKeyRotate: POST /api/v1/shell/manifests/{id}/key/rotate
// (#261 K1, admin-plane). Issues a replacement fm- key; the previous key
// stays heartbeat-valid for web_shell.key_rotation_grace (default 24h) or
// until the first heartbeat carries the new key, whichever comes first.
// Plaintext returned exactly once.
func (s *Server) handleShellServiceKeyRotate(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    grace := s.cfg.Config.Shell.KeyRotationGrace
    if grace <= 0 {
        grace = 24 * time.Hour
    }
    keyID, plaintext, err := s.shellRegistry.RotateServiceKey(id, grace)
    if err != nil {
        s.writeShellServiceKeyError(w, id, err)
        return
    }
    writeShellKeyResponse(w, http.StatusOK, "ok", "service key rotated; store the new key now — it is shown exactly once", id, keyID, plaintext)
}

// handleShellServiceKeyRevoke: DELETE /api/v1/shell/manifests/{id}/key
// (#261 K1, admin-plane). Immediately invalidates the module's service key:
// heartbeats 403, the proxy degrades to the structured 503 module_key_revoked
// and the shell host greys the module via the unreachable mechanism.
func (s *Server) handleShellServiceKeyRevoke(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    if err := s.shellRegistry.RevokeServiceKey(id); err != nil {
        s.writeShellServiceKeyError(w, id, err)
        return
    }
    shell.WriteEnvelope(w, http.StatusOK, "ok", "service key revoked; module degrades to 503 until re-registered", id, false)
}

func (s *Server) writeShellServiceKeyError(w http.ResponseWriter, id string, err error) {
    slog.Warn("shell service key operation rejected", "id", id, "error", err)
    switch {
    case errors.Is(err, shell.ErrManifestStatic):
        shell.WriteEnvelope(w, http.StatusConflict, "manifest_static",
            "builtin/static manifest is gateway-owned and has no revocable service key", id, false)
    case errors.Is(err, shell.ErrNoServiceKey):
        shell.WriteEnvelope(w, http.StatusConflict, "no_service_key",
            "module registered with a module-supplied api_key (or already revoked); nothing to rotate or revoke", id, false)
    case errors.Is(err, shell.ErrManifestNotFound):
        shell.WriteEnvelope(w, http.StatusNotFound, "manifest_unknown", err.Error(), id, false)
    default:
        shell.WriteEnvelope(w, http.StatusInternalServerError, "service_key_error", err.Error(), id, false)
    }
}

// handleShellManifestHeartbeat: POST /api/v1/shell/manifests/{id}/heartbeat
// — keepalive that keeps the registration online. The body is OPTIONAL
// (#284): an empty body (or one with no manifest id) is a plain liveness
// keepalive; a body carrying the module's current manifest converges the
// stored ext fields (pages/searchProviders/notifications/home) atomically,
// with the same validation gates as registration and the same service-key
// retention (#261/#277 — no plaintext re-reveal, key_id stable). A manifest
// refresh that fails validation still counts for liveness (the module stays
// online and observable) but returns 400 so the drift is surfaced loudly,
// not silently swallowed.
func (s *Server) handleShellManifestHeartbeat(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    var m *config.ShellManifest
    body, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, maxShellBody))
    if readErr == nil && len(bytes.TrimSpace(body)) > 0 {
        var candidate config.ShellManifest
        if err := json.Unmarshal(body, &candidate); err != nil {
            shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request",
                fmt.Sprintf("heartbeat body is neither empty nor a valid manifest: %v", err), id, false)
            return
        }
        // An id-less body (e.g. "{}") is treated as a plain liveness ping so
        // existing keepalive senders that POST an empty JSON object keep
        // working; only a body that names a manifest is a refresh attempt.
        if candidate.ID != "" {
            m = &candidate
        }
    }
    if err := s.shellRegistry.HeartbeatWithManifest(id, m); err != nil {
        notRegistered := strings.Contains(err.Error(), "not registered")
        if m == nil || notRegistered {
            // Plain heartbeat against an unregistered id (or the not-found
            // arm of a refresh) is a 404.
            shell.WriteEnvelope(w, http.StatusNotFound, "manifest_unknown", err.Error(), id, false)
            return
        }
        // Refresh attempt failed validation/id-match: liveness still counts
        // (recorded via the plain path), the error is surfaced as 400.
        _ = s.shellRegistry.Heartbeat(id)
        slog.Warn("shell heartbeat manifest refresh rejected (liveness still recorded)",
            "id", id, "error", err)
        shell.WriteEnvelope(w, http.StatusBadRequest, "manifest_refresh_rejected", err.Error(), id, false)
        return
    }
    if m != nil {
        shell.WriteEnvelope(w, http.StatusOK, "ok", "heartbeat accepted; manifest refreshed", id, false)
        return
    }
    shell.WriteEnvelope(w, http.StatusOK, "ok", "heartbeat accepted", id, false)
}

// handleShellManifestDelete: DELETE /api/v1/shell/manifests/{id} (#253) —
// manifest deregistration behind the same admin-plane gate as upsert. Only
// dynamic registrations are API-owned: an unknown id is 404
// manifest_unknown, a config.yaml static entry is 409 manifest_static (it
// would keep rendering offline from config — a 200 would be a lie).
func (s *Server) handleShellManifestDelete(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    if err := s.shellRegistry.Remove(id); err != nil {
        slog.Warn("shell manifest delete rejected", "id", id, "error", err)
        switch {
        case errors.Is(err, shell.ErrManifestStatic):
            shell.WriteEnvelope(w, http.StatusConflict, "manifest_static",
                "static manifest is config-owned (web_shell.manifests); remove it from config.yaml instead", id, false)
        default:
            shell.WriteEnvelope(w, http.StatusNotFound, "manifest_unknown", err.Error(), id, false)
        }
        return
    }
    shell.WriteEnvelope(w, http.StatusOK, "ok", "manifest removed", id, false)
}
