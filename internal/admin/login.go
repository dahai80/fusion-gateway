package admin

import (
    "encoding/json"
    "fmt"
    "log/slog"
    "net/http"
    "time"
)

type LoginRequest struct {
    Username string `json:"username"`
    Password string `json:"password"`
}

type LoginResponse struct {
    Username string `json:"username"`
    Role     string `json:"role"`
    Token    string `json:"token"`
}

// A1 fix: HandleLogin moved to AdminAuth receiver, reads from struct fields
func (a *AdminAuth) HandleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    if !a.Enabled() {
        slog.Error("admin login attempt but admin module not configured")
        writeError(w, http.StatusServiceUnavailable, "admin module not configured")
        return
    }

    // R6 (audit): cap the login request body. The bare json.Decode accepted an
    // unbounded body — a multi-MB POST against /admin/api/login burned memory
    // for a field set that's a few hundred bytes max. 4 KiB is generous for
    // username+password JSON; excess surfaces as a 400 (MaxBytesReader wraps
    // the read error), not an OOM.
    r.Body = http.MaxBytesReader(w, r.Body, 4096)

    var req LoginRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid JSON")
        return
    }

    // R6 (audit): lockout gate BEFORE the bcrypt compare. A locked-out username
    // gets 429 (not 401) so a brute-forcer can't distinguish "locked" from
    // "wrong password" by timing — both are refused, but the lockout surfaces
    // the throttle distinctly for monitoring.
    if locked, remaining := a.isLockedOut(req.Username); locked {
        slog.Warn("admin login refused, account locked out",
            "username", req.Username, "remaining", remaining.Round(time.Second))
        w.Header().Set("Retry-After", fmt.Sprintf("%d", int(remaining.Seconds())))
        writeError(w, http.StatusTooManyRequests, "account temporarily locked, try again later")
        return
    }

    if !a.Authenticate(req.Username, req.Password) {
        a.recordLoginFailure(req.Username)
        slog.Warn("admin login failed", "username", req.Username)
        writeError(w, http.StatusUnauthorized, "invalid credentials")
        return
    }

    a.recordLoginSuccess(req.Username)

    token, err := a.GenerateToken(req.Username, "admin")
    if err != nil {
        slog.Error("failed to generate admin token", "error", err)
        writeError(w, http.StatusInternalServerError, "token generation failed")
        return
    }

    http.SetCookie(w, &http.Cookie{
        Name:     "admin_token",
        Value:    token,
        Path:     "/",
        MaxAge:   86400,
        HttpOnly: true,
        Secure:   !a.insecureCookie,
        SameSite: http.SameSiteStrictMode,
    })

    writeJSON(w, http.StatusOK, LoginResponse{
        Username: req.Username,
        Role:     "admin",
        Token:    token,
    })
}

func (a *AdminAuth) HandleLogout(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }
    http.SetCookie(w, &http.Cookie{
        Name:     "admin_token",
        Value:    "",
        Path:     "/",
        MaxAge:   -1,
        HttpOnly: true,
        Secure:   !a.insecureCookie,
        SameSite: http.SameSiteStrictMode,
    })
    slog.Info("admin logout, cookie cleared", "remote", r.RemoteAddr)
    writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
