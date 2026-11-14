package server

import (
    "bytes"
    "crypto/sha256"
    "crypto/subtle"
    "encoding/hex"
    "encoding/json"
    "context"
    "errors"
    "fmt"
    "io"
    "log/slog"
    "net/http"
    "net/url"
    "os"
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
// buildShellSessionSecret derives the session signing secret: the explicit
// shell.session.secret wins; otherwise a domain-separated digest of the
// master_key (never the raw master key itself). #643: the derived digest is
// PERSISTED to <data_dir>/shell_session_secret (0600) on first boot and
// reloaded from the file afterwards — the effective master_key can flip
// between the env override (FUSION_ADMIN_KEY) and config.yaml across restart
// paths (fusion-sv sets the env; a direct start.sh does not), which silently
// changed the digest and invalidated every live session mid-walkthrough
// (signature errors, not TTL). The persisted value keeps sessions valid
// across such source flips; genuine key rotation means setting
// shell.session.secret explicitly (the config branch wins over the file).
// dataDir "" (tests / non-persistent setups) skips persistence entirely.
func buildShellSessionSecret(dataDir string, cfg *config.Config) ([]byte, string) {
    if s := strings.TrimSpace(cfg.Shell.Session.Secret); s != "" {
        return []byte(s), "config file"
    }
    derived := func() []byte {
        sum := sha256.Sum256([]byte(cfg.Auth.MasterKey + ":shell-session-v1"))
        return []byte(hex.EncodeToString(sum[:]))
    }
    if dataDir == "" {
        return derived(), "derived from auth.master_key (domain-separated digest)"
    }
    dir := config.ExpandPath(dataDir)
    path := filepath.Join(dir, "shell_session_secret")
    if b, err := os.ReadFile(path); err == nil {
        if trimmed := bytes.TrimSpace(b); len(trimmed) > 0 {
            return trimmed, "persisted file"
        }
    }
    secret := derived()
    if err := os.MkdirAll(dir, 0700); err != nil {
        slog.Warn("shell session secret persist skipped (mkdir failed; per-boot digest in use — restarts with a flipped master_key source will invalidate live sessions, issue #643)",
            "path", path, "error", err)
        return secret, "derived from auth.master_key (domain-separated digest)"
    }
    if err := os.WriteFile(path, secret, 0600); err != nil {
        slog.Warn("shell session secret persist failed (per-boot digest in use — restarts with a flipped master_key source will invalidate live sessions, issue #643)",
            "path", path, "error", err)
        return secret, "derived from auth.master_key (domain-separated digest)"
    }
    return secret, "derived from auth.master_key (persisted to file, stable across restarts)"
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
    secret, secretSource := buildShellSessionSecret(cfg.Store.DataDir, &cfg)
    // #611: log the secret SOURCE in every branch — tooling that mints
    // session tokens must be able to tell from the log which value to derive
    // from, especially with FUSION_ADMIN_KEY overriding auth.master_key.
    sum := sha256.Sum256(secret)
    fp := "sha256:" + hex.EncodeToString(sum[:8])
    if secretSource == "config file" {
        slog.Info("shell session secret resolved", "source", secretSource, "fingerprint", fp)
    } else {
        slog.Info("shell session secret resolved",
            "source", secretSource,
            "note", "the effective master_key source is logged at boot as auth.master_key resolved",
            "fingerprint", fp)
    }
    s.shellSessions = shell.NewManager(secret, cfg.Shell.Session.TTL)
    // #587: the failed-login lockout state lives with the session subsystem.
    s.shellLoginThrottle = newShellLoginThrottle()
    // #592: single-use SVG challenges for the login plane. The store is
    // ALWAYS built (#617): the enabled flag is consulted live from the
    // config snapshot on every challenge/login, so a reload can flip the
    // plane without a restart. TTL stays boot-time (store-level).
    {
        ttl := cfg.Shell.Captcha.TTL
        if ttl <= 0 {
            ttl = 5 * time.Minute
        }
        s.shellCaptcha = newShellCaptchaStore(ttl)
    }
    s.shellRegistry = shell.NewRegistry(cfg.Shell.Manifests, cfg.Shell.EntryOriginAllowlist, cfg.Shell.HeartbeatTimeout)
    // #478: feed the static proxy upstream map into the registry so a
    // self-registered upstream that disagrees with web_shell.modules WARNs
    // at registration instead of silently mis-routing the iframe document.
    staticUpstreams := make(map[string]string, len(cfg.Shell.Modules))
    for id, m := range cfg.Shell.Modules {
        if m.Upstream != "" {
            staticUpstreams[id] = m.Upstream
        }
    }
    s.shellRegistry.SetProxyUpstreams(staticUpstreams)
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
    // #669: when the upstream 401s the injected dynamic credential, adopt
    // the static web_shell.modules api_key as authoritative — the module
    // regenerated its own credential across a restart and never consumed
    // the gateway-issued plaintext, so the "dynamic wins" preference (#294)
    // is permanently divergent without this self-heal. Guards live in
    // ConvergeCredential (only the still-authoritative failed key swaps;
    // static never converges back, so no flip-flop).
    s.shellProxy.SetCredentialConverger(func(moduleID, failedKey string) {
        if m, ok := s.cfg.Config.Shell.Modules[moduleID]; ok && m.APIKey != "" {
            s.shellRegistry.ConvergeCredential(moduleID, failedKey, m.APIKey)
        }
    })
    s.setupShellUsers(&cfg)
    s.setupShellAdminSurfaces(&cfg)
    s.autoRegisterAdminModule()
    slog.Info("shell subsystem initialized",
        "static_manifests", len(cfg.Shell.Manifests),
        "modules", len(cfg.Shell.Modules),
        "users", s.shellUsers.Count(),
        "bootstrap_admin", s.shellBootstrap != nil)
}

// RefreshShellStaticManifests re-applies web_shell.manifests after a config
// hot-reload (#453). The registry is constructed once at server setup; without
// this a module added at runtime was proxy-reachable but invisible in the
// sidebar until a restart. No-op when the shell subsystem is disabled.
func (s *Server) RefreshShellStaticManifests(manifests []config.ShellManifest) {
    if s.shellRegistry == nil {
        return
    }
    s.shellRegistry.ReplaceStatic(manifests)
    slog.Info("shell static manifests refreshed on config reload", "count", len(manifests))
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
        Sort:     m.Sort,
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
    // #592: challenge issuance is unauthenticated by contract (the shell
    // frontend fetches it before any session exists) and must never be cached.
    mux.HandleFunc("GET /api/v1/shell/session/captcha", s.handleShellCaptchaChallenge)
    // #517: identity SSO ticket exchange → shell session (no second login).
    mux.HandleFunc("POST /api/v1/shell/session/sso", s.handleShellSessionSSO)
    mux.HandleFunc("POST /api/v1/shell/session/logout", s.withShellSession(s.handleShellSessionLogout))
    mux.HandleFunc("GET /api/v1/shell/me", s.withShellSession(s.handleShellMe))
    mux.HandleFunc("GET /api/v1/shell/manifests", s.withShellSession(s.handleShellManifestList))
    // #267: session-gated chat completions for the shell Home delegation
    // entry — same LLM router as /v1/chat/completions, but the credential
    // is the shell session JWT (the shell must not hold a gateway API key).
    mux.HandleFunc("POST /api/v1/shell/chat/completions", s.withShellSession(s.handleChatCompletions))
    // #443: session-gated models catalog for the shell Home model picker —
    // same handler as /v1/models (local loaded-first + cloud backends +
    // models.aliases), but the credential is the shell session JWT. The
    // picker previously read fusion-mlx /v1/models through the module proxy,
    // which lists loaded local models only — it could not offer models the
    // #267 shell chat can actually route to.
    mux.HandleFunc("GET /api/v1/shell/models", s.withShellSession(s.handleModels))
    // #487/#491 model-policy read plane for shell-session modules; the
    // /api/v1/model-policy alias serves consumers polling the non-sh-prefixed
    // path (best-effort, fail-open contract).
    mux.HandleFunc("GET /api/v1/shell/model-policy", s.withShellSession(s.handleShellModelPolicy))
    mux.HandleFunc("GET /api/v1/model-policy", s.withShellSession(s.handleShellModelPolicy))
    mux.HandleFunc("POST /api/v1/shell/manifests", s.withShellManifestUpsertAuth(s.handleShellManifestUpsert))
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
    mux.HandleFunc("/shell/", s.withShellProxyAuth(s.shellProxy.ServeHTTP))
    slog.Info("shell routes registered",
        "control_plane", "/api/v1/shell/*",
        "proxy", "/shell/{module_id}/")
}

// withShellProxyAuth guards /shell/<module_id>/* proxy requests on two
// credential planes. Session plane (unchanged): a shell session token, with
// the module-permission check. Service-key plane (#530): the module's own
// gateway-issued fm- key — the backend modules accept that key direct, so
// the gateway hop must not be stricter than the module behind it; the key is
// bound to the module id in the path via a hash compare (the same registry
// entry that persists across restarts), so a previously-issued key keeps
// working after a restart and a revoked key never authenticates. A key that
// authenticates IS the module speaking for itself — the user permission
// matrix does not apply.
func (s *Server) withShellProxyAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        moduleID, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/shell/"), "/")
        token := bearerToken(r)
        if token != "" && shell.IsModuleServiceKey(token, moduleID) && s.shellRegistry != nil {
            sum := sha256.Sum256([]byte(token))
            grace := s.cfg.Config.Shell.KeyRotationGrace
            if grace <= 0 {
                grace = 24 * time.Hour
            }
            if state, ok := s.shellRegistry.AuthServiceKey(moduleID, hex.EncodeToString(sum[:]), grace); ok {
                slog.Debug("shell proxy request authenticated by module service key", "module", moduleID, "state", state)
                next(w, r)
                return
            }
            slog.Info("shell proxy denied: module service key rejected", "module", moduleID, "path", r.URL.Path)
            shell.WriteEnvelope(w, http.StatusUnauthorized, "service_key_invalid",
                "module service key rejected for this module", moduleID, false)
            return
        }
        s.withShellSession(s.withShellModuleAccess(next)).ServeHTTP(w, r)
    }
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

// shellLoginRedirect (#451) bounces an anonymous / expired-session browser
// navigation to the SPA login with a same-origin return path. A customer
// arriving from the marketing site has no session and no fetch interceptor
// to translate the JSON 401 envelope, so a navigation-shaped request must
// never receive raw JSON. API-shaped calls (fetch/XHR, service keys) keep
// the envelope — the SPA intercepts those.
func (s *Server) shellLoginRedirect(w http.ResponseWriter, r *http.Request) {
    returnPath := r.URL.RequestURI()
    if strings.HasPrefix(returnPath, "//") {
        returnPath = "/"
    }
    target := "/login?return=" + url.QueryEscape(returnPath)
    http.Redirect(w, r, target, http.StatusFound)
}

// withShellSession guards a handler behind a valid shell session Bearer
// token. 401 with the standardized envelope — the shell host intercepts 401
// and bounces to the login page (PRD §4.1.4). Browser navigations (#451)
// get a same-origin 302 to /login?return=... instead of the envelope.
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
            if s.spaRegistered && spaFallbackRequest(r) {
                slog.Info("shell anonymous navigation redirected to login", "path", r.URL.Path)
                s.shellLoginRedirect(w, r)
                return
            }
            w.Header().Set("WWW-Authenticate", `Bearer realm="fusion-shell"`)
            shell.WriteEnvelope(w, http.StatusUnauthorized, "session_required", "shell session token required", "", false)
            return
        }
        claims, err := s.shellSessions.Validate(token)
        if err != nil {
            slog.Info("shell session rejected", "path", r.URL.Path, "error", err)
            if s.spaRegistered && spaFallbackRequest(r) {
                s.shellLoginRedirect(w, r)
                return
            }
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
// handleShellCaptchaChallenge (#592) serves a fresh single-use SVG challenge
// for the login plane. Unauthenticated by contract; always no-store.
func (s *Server) handleShellCaptchaChallenge(w http.ResponseWriter, r *http.Request) {
    if s.shellSessions == nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "shell_disabled", "shell subsystem is not enabled", "", false)
        return
    }
    serveCaptchaChallenge(w, s.captchaStoreLive())
}

// captchaStoreLive (#617) resolves the login-plane challenge store against
// the LIVE config snapshot: nil when the plane is disabled by the current
// config, so the challenge endpoints answer disabled and the login gates
// skip verification without a restart. The store itself is always built at
// setup (see setupShell) so an off→on reload arms the gate immediately.
func (s *Server) captchaStoreLive() *shellCaptchaStore {
    s.cfgMu.RLock()
    enabled := s.cfg.Config.Shell.Captcha.Enabled
    s.cfgMu.RUnlock()
    if !enabled || s.shellCaptcha == nil {
        return nil
    }
    return s.shellCaptcha
}

func (s *Server) handleShellSessionIssue(w http.ResponseWriter, r *http.Request) {
    if s.shellSessions == nil {
        shell.WriteEnvelope(w, http.StatusNotFound, "shell_disabled", "shell subsystem is not enabled", "", false)
        return
    }
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        Username    string `json:"username"`
        Password    string `json:"password"`
        CaptchaID   string `json:"captcha_id"`
        CaptchaCode string `json:"captcha_code"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil || req.Username == "" {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be JSON with username and password", "", false)
        return
    }

    // #587: the login endpoint is the only public auth oracle — throttle
    // failed logins per source IP + per username. The lockout check runs
    // BEFORE credential verification so a locked source gets 429 even for a
    // correct password (no oracle left at all during lockout).
    ip := loginClientIP(r)
    if s.shellLoginThrottle == nil {
        s.shellLoginThrottle = newShellLoginThrottle()
    }
    if okLock, wait := s.shellLoginThrottle.allowed("ip:"+ip, "user:"+req.Username); !okLock {
        slog.Warn("shell login throttled: source locked out", "ip", ip, "username", req.Username, "retry_after", wait.String())
        shell.WriteEnvelope(w, http.StatusTooManyRequests, "rate_limited",
            "too many failed logins; retry later", "", true)
        if s.shellAudit != nil {
            _ = s.shellAudit.Record(req.Username, "", shell.AuditClassAuth, http.MethodPost,
                "/api/v1/shell/session", shell.AuditOutcomeDenied, 0)
        }
        return
    }

    // #592: the CAPTCHA gate runs BEFORE credential verification — a wrong
    // or missing challenge never reveals whether the password would have
    // matched. Any captcha_* failure is audit-logged but does NOT feed the
    // #587 login-lockout counters (a challenge guess is not a password
    // guess; feeding it would let a bot farm lockouts cheaply).
    if st := s.captchaStoreLive(); st != nil {
        if vCode := st.verify(req.CaptchaID, req.CaptchaCode); vCode != "" {
            slog.Warn("shell login captcha rejected", "code", vCode, "ip", ip, "username", req.Username)
            if s.shellAudit != nil {
                _ = s.shellAudit.Record(req.Username, "", shell.AuditClassAuth, http.MethodPost,
                    "/api/v1/shell/session", shell.AuditOutcomeDenied, 0)
            }
            shell.WriteEnvelope(w, http.StatusBadRequest, vCode,
                "captcha verification failed; request a new challenge", "", true)
            return
        }
    }

    user, ok := s.resolveShellUser(req.Username)
    if !ok || !checkShellPassword(user, req.Password) {
        s.shellLoginThrottle.recordFailure(ip, req.Username)
        slog.Warn("shell session login failed", "username", req.Username, "remote", r.RemoteAddr)
        if s.shellAudit != nil {
            _ = s.shellAudit.Record(req.Username, "", shell.AuditClassAuth, http.MethodPost,
                "/api/v1/shell/session", shell.AuditOutcomeDenied, 0)
        }
        shell.WriteEnvelope(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password", "", false)
        return
    }

    // Issue #543: the tenant comes from shell.session.tenant (default
    // "local"), not a hard-coded id — a mixed password/SSO deployment points
    // it at the SSO tenant so both login methods share one policy namespace.
    tenant := s.cfg.Config.Shell.Session.Tenant
    if tenant == "" {
        tenant = "local"
    }
    token, err := s.shellSessions.Issue(shell.SessionClaims{
        Sub:                user.username,
        Roles:              user.roles,
        Modules:            user.modules,
        TenantID:           tenant,
        Origin:             shell.OriginLocal,
        MustChangePassword: user.mustChange,
    })
    if err != nil {
        slog.Error("shell session issue failed", "username", user.username, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "session_issue_failed", "failed to issue session token", "", true)
        return
    }
    slog.Info("shell session issued", "username", user.username, "roles", user.roles)
    // #587: a successful login clears the source's failure counters so a
    // legitimate user's occasional typo never accumulates into a lockout.
    if s.shellLoginThrottle != nil {
        s.shellLoginThrottle.recordSuccess(loginClientIP(r), user.username)
    }
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
        TenantID:           tenant,
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
// (env FG_SHELL_ADMIN_KEY canonical per #409; legacy FUSION_GATEWAY_ADMIN_KEY
// bound as a transition alias for the credential downstream modules
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
// #436: the route is wired OUTSIDE withMiddleware (same as heartbeat) — the
// standard fg-key chain 401'd the fm- Bearer before this gate could evaluate
// it, so the #369 self-heal loop was unreachable. The admin plane is checked
// from the raw Bearer token here (constant-time, same helper as heartbeat),
// since no middleware principal is resolved on this path.
// shellAuthPlaneKey marks HOW a manifest-upsert request authenticated
// (#460): the admin plane proves nothing about the module holding its
// issued service key, while the #369 self-heal path authenticates with the
// valid fm- key itself. The handler uses it to decide between rotating the
// service key (admin plane) and retaining it (self-heal).
type shellAuthPlaneKey struct{}

const (
    shellAuthPlaneAdmin      = "admin"
    shellAuthPlaneServiceKey = "service_key"
)

func shellAuthPlaneFromContext(ctx context.Context) string {
    v, _ := ctx.Value(shellAuthPlaneKey{}).(string)
    return v
}

// #484: withShellHeartbeatAuth records which service-key shape matched
// (shell.ServiceKeyStateCurrent or ServiceKeyStateStale) so the heartbeat
// handler can flag a still-grace module. Absent for admin-token heartbeats.
type shellKeyStateKey struct{}

func shellKeyStateFromContext(ctx context.Context) string {
    v, _ := ctx.Value(shellKeyStateKey{}).(string)
    return v
}

func (s *Server) withShellManifestUpsertAuth(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if s.shellHeartbeatAdminToken(shellBearerToken(r)) {
            next(w, r.WithContext(context.WithValue(r.Context(), shellAuthPlaneKey{}, shellAuthPlaneAdmin)))
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
        // #493: a per-module registered service key (web_shell.service_keys,
        // overlayed from FUSION_SERVICE_KEY_<MODULE> envs) authenticates that
        // module's own registration — same scoped plane as the fm- self-heal
        // below, never a general admin plane.
        if json.Unmarshal(body, &probe) == nil && probe.ID != "" && s.serviceKeyAllowed(r, probe.ID) {
            slog.Info("shell manifest re-register authenticated by registered service key (#493)", "id", probe.ID)
            r.Body = io.NopCloser(bytes.NewReader(body))
            r.ContentLength = int64(len(body))
            next(w, r.WithContext(context.WithValue(r.Context(), shellAuthPlaneKey{}, shellAuthPlaneServiceKey)))
            return
        }
        if json.Unmarshal(body, &probe) == nil && probe.ID != "" &&
            shell.IsServiceKeyPlaintext(token) && s.shellRegistry != nil {
            sum := sha256.Sum256([]byte(token))
            grace := s.cfg.Config.Shell.KeyRotationGrace
            if grace <= 0 {
                grace = 24 * time.Hour
            }
            if _, ok := s.shellRegistry.AuthServiceKey(probe.ID, hex.EncodeToString(sum[:]), grace); ok {
                slog.Info("shell manifest re-register authenticated by module service key (#369 self-heal)", "id", probe.ID)
                r.Body = io.NopCloser(bytes.NewReader(body))
                r.ContentLength = int64(len(body))
                next(w, r.WithContext(context.WithValue(r.Context(), shellAuthPlaneKey{}, shellAuthPlaneServiceKey)))
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
        // #601: schema-validation failures are client payload errors — 400,
        // not 403 (auth already passed; the caller fixed nothing by retrying
        // with different credentials). The A7 entry-origin allowlist
        // rejection stays 403: it is a policy denial, not a malformed body.
        status := http.StatusBadRequest
        if errors.Is(err, shell.ErrEntryOriginNotAllowed) {
            status = http.StatusForbidden
        }
        shell.WriteEnvelope(w, status, "manifest_rejected", err.Error(), m.ID, false)
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
        // #460: the two auth planes mean opposite things for key ambiguity.
        // Admin plane: the module just proved it does NOT hold a working
        // key (it authenticated with the admin credential), so keep-your-key
        // vs your-key-is-dead is undecidable for the client — rotate and
        // return the new plaintext so the re-register always leaves the
        // module with a working key. Self-heal plane (#369): the module
        // authenticated WITH its valid fm- key, so retain without
        // re-revealing (issue-once stands; rotating on every re-register
        // would churn keys for a demonstrably healthy module).
        if shellAuthPlaneFromContext(r.Context()) == shellAuthPlaneAdmin {
            // #468: rotate only when the module has demonstrated consumption
            // (it presented its fm- key at least once — heartbeat auth). A
            // module that never presents the key (fusion-mlx: the proxy
            // injects the credential) re-registers as a keep-alive every
            // ~15s; rotating on each pass churned 246 keys in 44 minutes
            // and nothing ever consumed the plaintext.
            if !s.shellRegistry.ServiceKeyConsumed(m.ID) {
                if allowed, suppressed := rotateGateLog.AllowWithSuppressed(m.ID); allowed {
                    slog.Info("shell service key rotation suppressed on admin re-register (key never consumed, #468)",
                        "id", m.ID, "suppressed", suppressed)
                }
                writeShellKeyResponse(w, http.StatusOK, "ok", "manifest registered (service key retained; raw key shown only at issuance)", m.ID, keyID, "")
                return
            }
            grace := s.cfg.Config.Shell.KeyRotationGrace
            if grace <= 0 {
                grace = 24 * time.Hour
            }
            rotID, rotPlaintext, err := s.shellRegistry.RotateServiceKey(m.ID, grace)
            if err != nil {
                // Builtins/static and no-existing-key cases keep the
                // legacy shape (their credentials are config-owned or
                // issuance already handled them above).
                slog.Warn("shell service key rotation on admin re-register skipped",
                    "id", m.ID, "error", err)
                writeShellKeyResponse(w, http.StatusOK, "ok", "manifest registered (service key retained; raw key shown only at issuance)", m.ID, keyID, "")
                return
            }
            slog.Info("shell service key rotated on admin re-register (#460)", "id", m.ID, "key_id", rotID)
            writeShellKeyResponse(w, http.StatusOK, "ok", "manifest registered; service key rotated — store it now, it is shown exactly once", m.ID, rotID, rotPlaintext)
            return
        }
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

// rotateGateLog throttles the #468 rotation-suppression INFO (1 per 5m per
// module): keep-alive re-registers arrive every ~15s per source, and the
// suppressed-count carries the churn volume without flooding the log.
var rotateGateLog = logthrottle.New(5 * time.Minute)

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
            if state, ok := s.shellRegistry.AuthServiceKey(id, hex.EncodeToString(sum[:]), grace); ok {
                // #484: carry the match state to the handler so the 200
                // response can tell a still-grace module its key is stale
                // (the proxy injects the NEW key; the module holding the
                // old one sees every proxied call fail with Unauthorized
                // while this heartbeat keeps returning 200).
                next(w, r.WithContext(context.WithValue(r.Context(), shellKeyStateKey{}, state)))
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
    // #484: a fm- key that matched inside the rotation grace window means
    // the module is holding a rotated-out key — surface it so the module
    // can re-register proactively instead of discovering the rotation
    // through Unauthorized failures on every proxied call. Header is the
    // machine-readable signal; the message spells it out for humans.
    if state := shellKeyStateFromContext(r.Context()); state != "" {
        w.Header().Set("X-Fusion-Key-State", state)
        if state == shell.ServiceKeyStateStale {
            if m != nil {
                shell.WriteEnvelope(w, http.StatusOK, "ok", "heartbeat accepted; manifest refreshed; service key is STALE (rotated out, grace window) — re-register via POST /api/v1/shell/manifests to pick up the new key", id, false)
                return
            }
            shell.WriteEnvelope(w, http.StatusOK, "ok", "heartbeat accepted; service key is STALE (rotated out, grace window) — re-register via POST /api/v1/shell/manifests to pick up the new key", id, false)
            return
        }
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
