package server

import (
    "crypto/rand"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "strings"

    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// RBAC user APIs (PRD R-1 ③) backing the shell host admin page: list /
// create / update / delete / password-reset under shell-session auth with
// the admin role required. The UserStore file is the source of truth once
// seeded; config users are initial seed data only.
//
// Guard rails: the last enabled admin cannot be disabled, demoted, or
// deleted — otherwise an operator could lock themselves out of user
// management entirely. Password material is bcrypt-only on the wire.

const minShellPassword = 8

// requireShellAdmin wraps a shell-session handler with the admin-role gate
// (EI9 lesson: role checks server-side, never client-only).
func requireShellAdmin(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        claims := shellClaimsFrom(r.Context())
        if !hasAnyRole(claims.Roles, "admin") {
            slog.Warn("shell users API denied (non-admin)", "path", r.URL.Path, "user", claims.Sub, "roles", claims.Roles)
            shell.WriteEnvelope(w, http.StatusForbidden, "forbidden", "admin role required", "", false)
            return
        }
        next(w, r)
    }
}

func hasAnyRole(roles []string, want string) bool {
    for _, r := range roles {
        if r == want {
            return true
        }
    }
    return false
}

// validateShellRoles returns the role names that do not resolve in the role
// store (built-in or custom). A phantom role is invisible in the roles admin
// surface and unmanageable (#359) — mirrors the menu-permissions Put gate in
// internal/shell/roles.go.
func (s *Server) validateShellRoles(roles []string) []string {
    if s.shellRoles == nil {
        return nil
    }
    var unknown []string
    for _, r := range roles {
        if !s.shellRoles.Exists(r) {
            unknown = append(unknown, r)
        }
    }
    return unknown
}

// shellAdminRow is the AdminUserRow wire shape the shell host admin page
// consumes (never exposes password material).
type shellAdminRow struct {
    UserID             string   `json:"user_id"`
    DisplayName        string   `json:"display_name"`
    Roles              []string `json:"roles"`
    Disabled           bool     `json:"disabled"`
    MustChangePassword bool     `json:"must_change_password"`
    Permissions        []string `json:"permissions"`
}

func toShellAdminRow(a shell.UserAccount) shellAdminRow {
    return shellAdminRow{
        UserID:             a.Username,
        DisplayName:        a.DisplayName,
        Roles:              a.Roles,
        Disabled:           a.Disabled,
        MustChangePassword: a.MustChangePassword,
        Permissions:        a.Permissions,
    }
}

// handleShellUserList: GET /api/v1/shell/users.
func (s *Server) handleShellUserList(w http.ResponseWriter, _ *http.Request) {
    accounts := s.shellUsers.List()
    rows := make([]shellAdminRow, 0, len(accounts))
    for _, a := range accounts {
        rows = append(rows, toShellAdminRow(a))
    }
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(rows)
}

// handleShellUserCreate: POST /api/v1/shell/users
// {username, display_name, password, permissions}.
func (s *Server) handleShellUserCreate(w http.ResponseWriter, r *http.Request) {
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        Username    string   `json:"username"`
        DisplayName string   `json:"display_name"`
        Password    string   `json:"password"`
        Permissions []string `json:"permissions"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be JSON", "", false)
        return
    }
    req.Username = strings.TrimSpace(req.Username)
    if req.Username == "" {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "username is required", "", false)
        return
    }
    if len(req.Password) < minShellPassword {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters", "", false)
        return
    }
    // The account is created with the default role set — validate it against
    // the role store so the default never drifts out of the resolvable set.
    roles := []string{"user"}
    if unknown := s.validateShellRoles(roles); len(unknown) > 0 {
        slog.Warn("shell user create rejected unknown roles", "username", req.Username, "roles", unknown)
        shell.WriteEnvelope(w, http.StatusBadRequest, "role_rejected", fmt.Sprintf("unknown roles: %s", strings.Join(unknown, ", ")), "", false)
        return
    }
    if _, exists := s.shellUsers.Get(req.Username); exists {
        shell.WriteEnvelope(w, http.StatusConflict, "user_exists", fmt.Sprintf("user %q already exists", req.Username), "", false)
        return
    }
    hash, err := hashShellPassword(req.Password)
    if err != nil {
        slog.Error("shell user create hash failed", "username", req.Username, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_create_failed", "failed to hash password", "", true)
        return
    }
    displayName := req.DisplayName
    if displayName == "" {
        displayName = req.Username
    }
    account := shell.UserAccount{
        Username:     req.Username,
        DisplayName:  displayName,
        PasswordHash: hash,
        Roles:        roles,
        Permissions:  req.Permissions,
    }
    if err := s.shellUsers.Create(account); err != nil {
        slog.Error("shell user create failed", "username", req.Username, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_create_failed", err.Error(), "", false)
        return
    }
    slog.Info("shell user created", "username", req.Username, "by", shellClaimsFrom(r.Context()).Sub, "permissions", req.Permissions)
    writeShellAdminRow(w, http.StatusCreated, account)
}

// handleShellUserUpdate: PATCH /api/v1/shell/users/{id} with a partial body
// {display_name?, disabled?, permissions?, roles?} (username immutable).
func (s *Server) handleShellUserUpdate(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        DisplayName *string   `json:"display_name"`
        Disabled    *bool     `json:"disabled"`
        Permissions *[]string `json:"permissions"`
        Roles       *[]string `json:"roles"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be JSON", "", false)
        return
    }
    account, ok := s.shellUsers.Get(id)
    if !ok {
        shell.WriteEnvelope(w, http.StatusNotFound, "user_not_found", fmt.Sprintf("user %q not found", id), "", false)
        return
    }
    if req.Disabled != nil && *req.Disabled && !account.Disabled && hasAnyRole(account.Roles, "admin") && s.shellUsers.EnabledAdminCount() <= 1 {
        shell.WriteEnvelope(w, http.StatusConflict, "last_admin", "cannot disable the last enabled admin", "", false)
        return
    }
    if req.Roles != nil {
        // Roles land verbatim on the account — reject names that do not
        // resolve in the role store before the last-admin guard (#359).
        if unknown := s.validateShellRoles(*req.Roles); len(unknown) > 0 {
            slog.Warn("shell user update rejected unknown roles", "id", id, "roles", unknown)
            shell.WriteEnvelope(w, http.StatusBadRequest, "role_rejected", fmt.Sprintf("unknown roles: %s", strings.Join(unknown, ", ")), "", false)
            return
        }
        if !hasAnyRole(*req.Roles, "admin") && hasAnyRole(account.Roles, "admin") && !account.Disabled && s.shellUsers.EnabledAdminCount() <= 1 {
            shell.WriteEnvelope(w, http.StatusConflict, "last_admin", "cannot demote the last enabled admin", "", false)
            return
        }
    }
    if err := s.shellUsers.Update(id, func(a *shell.UserAccount) {
        if req.DisplayName != nil {
            a.DisplayName = *req.DisplayName
        }
        if req.Disabled != nil {
            a.Disabled = *req.Disabled
        }
        if req.Permissions != nil {
            a.Permissions = *req.Permissions
        }
        if req.Roles != nil {
            a.Roles = *req.Roles
        }
    }); err != nil {
        slog.Error("shell user update failed", "id", id, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_update_failed", err.Error(), "", false)
        return
    }
    slog.Info("shell user updated", "id", id, "by", shellClaimsFrom(r.Context()).Sub)
    updated, _ := s.shellUsers.Get(id)
    writeShellAdminRow(w, http.StatusOK, updated)
}

// handleShellUserDelete: DELETE /api/v1/shell/users/{id}. Hard delete here is
// the account row only — gateway API keys keep their disable/revoke semantics
// elsewhere (never touched by the shell RBAC surface).
func (s *Server) handleShellUserDelete(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    account, ok := s.shellUsers.Get(id)
    if !ok {
        shell.WriteEnvelope(w, http.StatusNotFound, "user_not_found", fmt.Sprintf("user %q not found", id), "", false)
        return
    }
    if !account.Disabled && hasAnyRole(account.Roles, "admin") && s.shellUsers.EnabledAdminCount() <= 1 {
        shell.WriteEnvelope(w, http.StatusConflict, "last_admin", "cannot delete the last enabled admin", "", false)
        return
    }
    if err := s.shellUsers.Delete(id); err != nil {
        slog.Error("shell user delete failed", "id", id, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_delete_failed", err.Error(), "", false)
        return
    }
    slog.Info("shell user deleted", "id", id, "by", shellClaimsFrom(r.Context()).Sub)
    w.WriteHeader(http.StatusNoContent)
}

// handleShellUserPasswordReset: POST /api/v1/shell/users/{id}/password-reset
// — mint a random temp password, bcrypt it, force a change on next login.
func (s *Server) handleShellUserPasswordReset(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
    if _, ok := s.shellUsers.Get(id); !ok {
        shell.WriteEnvelope(w, http.StatusNotFound, "user_not_found", fmt.Sprintf("user %q not found", id), "", false)
        return
    }
    temp, err := generateTempPassword()
    if err != nil {
        slog.Error("shell password reset entropy failed", "id", id, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_reset_failed", "failed to generate temp password", "", true)
        return
    }
    hash, err := hashShellPassword(temp)
    if err != nil {
        slog.Error("shell password reset hash failed", "id", id, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_reset_failed", "failed to hash temp password", "", true)
        return
    }
    if err := s.shellUsers.Update(id, func(a *shell.UserAccount) {
        a.PasswordHash = hash
        a.MustChangePassword = true
    }); err != nil {
        slog.Error("shell password reset failed", "id", id, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "user_reset_failed", err.Error(), "", false)
        return
    }
    slog.Info("shell user password reset", "id", id, "by", shellClaimsFrom(r.Context()).Sub)
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]string{"temp_password": temp})
}

// handleShellPasswordChange: POST /api/v1/password {password} — the caller's
// own password (first-login bootstrap rotation included). Clears
// must_change_password so /me stops flagging the forced change.
func (s *Server) handleShellPasswordChange(w http.ResponseWriter, r *http.Request) {
    claims := shellClaimsFrom(r.Context())
    body := http.MaxBytesReader(w, r.Body, maxShellBody)
    var req struct {
        Password string `json:"password"`
    }
    if err := json.NewDecoder(body).Decode(&req); err != nil {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "body must be JSON", "", false)
        return
    }
    if len(req.Password) < minShellPassword {
        shell.WriteEnvelope(w, http.StatusBadRequest, "invalid_request", "password must be at least 8 characters", "", false)
        return
    }
    hash, err := hashShellPassword(req.Password)
    if err != nil {
        slog.Error("shell password change hash failed", "user", claims.Sub, "error", err)
        shell.WriteEnvelope(w, http.StatusInternalServerError, "password_change_failed", "failed to hash password", "", true)
        return
    }
    if err := s.shellUsers.Update(claims.Sub, func(a *shell.UserAccount) {
        a.PasswordHash = hash
        a.MustChangePassword = false
    }); err != nil {
        // The bootstrap admin exists only in memory until its first password
        // change rotates it into the store — create on demand.
        if s.shellBootstrap != nil && s.shellBootstrap.username == claims.Sub {
            account := shell.UserAccount{
                Username:     s.shellBootstrap.username,
                DisplayName:  s.shellBootstrap.username,
                PasswordHash: hash,
                Roles:        s.shellBootstrap.roles,
                Permissions:  s.shellBootstrap.modules,
            }
            if cerr := s.shellUsers.Create(account); cerr != nil {
                slog.Error("shell bootstrap password rotation failed", "user", claims.Sub, "error", cerr)
                shell.WriteEnvelope(w, http.StatusInternalServerError, "password_change_failed", cerr.Error(), "", false)
                return
            }
        } else {
            slog.Error("shell password change failed", "user", claims.Sub, "error", err)
            shell.WriteEnvelope(w, http.StatusInternalServerError, "password_change_failed", err.Error(), "", false)
            return
        }
    }
    slog.Info("shell password changed", "user", claims.Sub)
    shell.WriteEnvelope(w, http.StatusOK, "ok", "password changed", "", false)
}

// generateTempPassword mints a 16-byte random base64url password (24 chars).
func generateTempPassword() (string, error) {
    raw := make([]byte, 16)
    if _, err := rand.Read(raw); err != nil {
        return "", fmt.Errorf("crypto/rand: %w", err)
    }
    return base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeShellAdminRow(w http.ResponseWriter, status int, a shell.UserAccount) {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    _ = json.NewEncoder(w).Encode(toShellAdminRow(a))
}
