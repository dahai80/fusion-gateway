package admin

import (
    "encoding/json"
    "log/slog"
    "net/http"
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

    var req LoginRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid JSON")
        return
    }

    if !a.Authenticate(req.Username, req.Password) {
        slog.Warn("admin login failed", "username", req.Username)
        writeError(w, http.StatusUnauthorized, "invalid credentials")
        return
    }

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
