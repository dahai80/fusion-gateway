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
    Token    string `json:"token"`
    Username string `json:"username"`
    Role     string `json:"role"`
}

var adminUsers map[string]string

func SetAdminUsers(users map[string]string) {
    adminUsers = users
}

func HandleLogin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        writeError(w, http.StatusMethodNotAllowed, "method not allowed")
        return
    }

    if !JWTSecretSet() {
        slog.Error("admin login attempt but JWT secret not configured")
        writeError(w, http.StatusServiceUnavailable, "admin module not configured")
        return
    }

    if len(adminUsers) == 0 {
        slog.Error("admin login attempt but no admin users configured")
        writeError(w, http.StatusServiceUnavailable, "admin module not configured")
        return
    }

    var req LoginRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        writeError(w, http.StatusBadRequest, "invalid JSON")
        return
    }

    password, ok := adminUsers[req.Username]
    if !ok || password != req.Password {
        slog.Warn("admin login failed", "username", req.Username)
        writeError(w, http.StatusUnauthorized, "invalid credentials")
        return
    }

    token, err := GenerateToken(req.Username, "admin")
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
        SameSite: http.SameSiteStrictMode,
    })

    writeJSON(w, http.StatusOK, LoginResponse{
        Token:    token,
        Username: req.Username,
        Role:     "admin",
    })
}
