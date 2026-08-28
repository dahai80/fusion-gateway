package admin

import (
    "errors"
    "fmt"
    "log/slog"
    "sync"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "golang.org/x/crypto/bcrypt"
)

// A1 fix: AdminAuth replaces global jwtSecret + adminUsers variables.
// Constructed via NewAdminAuth, injected into Server and Handler.
// #12 fix: passwords stored as bcrypt hashes, not plaintext.
type AdminAuth struct {
    mu             sync.RWMutex
    jwtSecret      []byte
    adminUsers     map[string]string // username -> bcrypt hash
    insecureCookie bool              // true when running without TLS
    // R6 (audit): per-username login attempt tracking + lockout. A username
    // key (not IP) survives shared-IP/NAT — a brute-force from behind one NAT
    // egress would otherwise share one lockout budget across all victims. The
    // attempt map is bounded by the distinct-usernames-ever-seen (small).
    loginMu      sync.Mutex
    loginAttempts map[string]*loginAttempt
}

// loginAttempt tracks consecutive failures + lockout for one username (R6).
type loginAttempt struct {
    count        int
    lockedUntil  time.Time
}

const (
    maxLoginAttempts   = 5
    loginLockoutDur    = 15 * time.Minute
)

func (a *AdminAuth) SetInsecureCookie(v bool) {
    a.insecureCookie = v
}

// ReloadUsers atomically replaces the in-memory admin user map (H8). Called
// after the admin config PUT handler persists new (already-hashed) passwords
// so a rotated password takes effect immediately — not only after the next
// process restart. users values must already be bcrypt hashes (the handler
// hashes before persisting). The current request continues under the old map;
// the next Authenticate call sees the new one.
func (a *AdminAuth) ReloadUsers(users map[string]string) {
    hashedUsers := make(map[string]string, len(users))
    for username, password := range users {
        if isBcryptHash(password) {
            hashedUsers[username] = password
        } else {
            hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
            if err != nil {
                slog.Error("admin ReloadUsers: failed to hash password, skipping user",
                    "username", username, "error", err)
                continue
            }
            hashedUsers[username] = string(hash)
        }
    }
    a.mu.Lock()
    a.adminUsers = hashedUsers
    a.mu.Unlock()
    slog.Info("admin users reloaded (rotation applied without restart)", "count", len(hashedUsers))
}

// ReloadSecret atomically replaces the JWT signing secret (H8). Called after
// the admin config PUT handler persists a new jwt_secret so a rotation takes
// effect immediately. Existing tokens signed with the old secret are
// invalidated (validate uses the new secret).
func (a *AdminAuth) ReloadSecret(secret string) {
    if len(secret) < 32 {
        slog.Warn("admin ReloadSecret: secret too short, keeping current secret", "len", len(secret))
        return
    }
    a.mu.Lock()
    a.jwtSecret = []byte(secret)
    a.mu.Unlock()
    slog.Info("admin jwt secret reloaded (rotation applied without restart)")
}

func NewAdminAuth(secret string, users map[string]string) (*AdminAuth, error) {
    if secret == "" {
        slog.Error("admin JWT secret is empty, admin module will be disabled")
        return &AdminAuth{}, nil
    }
    if len(secret) < 32 {
        return nil, fmt.Errorf("admin JWT secret must be at least 32 characters, got %d", len(secret))
    }

    // #12 fix: hash all plaintext passwords at startup
    hashedUsers := make(map[string]string, len(users))
    for username, password := range users {
        if isBcryptHash(password) {
            hashedUsers[username] = password
        } else {
            hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
            if err != nil {
                return nil, fmt.Errorf("failed to hash password for user %q: %w", username, err)
            }
            hashedUsers[username] = string(hash)
            slog.Info("admin password hashed at startup", "username", username)
        }
    }

    return &AdminAuth{
        jwtSecret:    []byte(secret),
        adminUsers:   hashedUsers,
        loginAttempts: make(map[string]*loginAttempt),
    }, nil
}

// isLockedOut reports whether username is currently in login lockout (R6).
// Returns the remaining lockout duration so the caller can surface it.
func (a *AdminAuth) isLockedOut(username string) (locked bool, remaining time.Duration) {
    if username == "" {
        return false, 0
    }
    a.loginMu.Lock()
    defer a.loginMu.Unlock()
    att, ok := a.loginAttempts[username]
    if !ok || att.count < maxLoginAttempts {
        return false, 0
    }
    remaining = time.Until(att.lockedUntil)
    if remaining <= 0 {
        // Lockout expired — reset so the next failure starts a fresh window.
        delete(a.loginAttempts, username)
        return false, 0
    }
    return true, remaining
}

// recordLoginFailure increments the failure counter for username and arms the
// lockout once the threshold is crossed (R6). No-op for empty username.
func (a *AdminAuth) recordLoginFailure(username string) {
    if username == "" {
        return
    }
    a.loginMu.Lock()
    defer a.loginMu.Unlock()
    att, ok := a.loginAttempts[username]
    if !ok {
        att = &loginAttempt{}
        a.loginAttempts[username] = att
    }
    att.count++
    if att.count >= maxLoginAttempts {
        att.lockedUntil = time.Now().Add(loginLockoutDur)
        slog.Warn("admin login locked out after repeated failures",
            "username", username, "attempts", att.count, "locked_for", loginLockoutDur)
    }
}

// recordLoginSuccess clears the attempt history for username (R6).
func (a *AdminAuth) recordLoginSuccess(username string) {
    if username == "" {
        return
    }
    a.loginMu.Lock()
    defer a.loginMu.Unlock()
    delete(a.loginAttempts, username)
}

// isBcryptHash checks if a string looks like a pre-computed bcrypt hash ($2a$, $2b$, $2y$)
func isBcryptHash(s string) bool {
    return len(s) == 60 && (s[:4] == "$2a$" || s[:4] == "$2b$" || s[:4] == "$2y$")
}

// HashAdminPasswordIfPlaintext returns a bcrypt hash of password unless it is
// already a bcrypt hash (in which case it is returned unchanged). H8: the admin
// config PUT handler wrote raw passwords to config.yaml and relied on a startup
// bcrypt pass to hash them — so a password set via the admin API sat in
// plaintext on disk until the next process restart, and a crash/replay before
// restart exposed it. Hashing at the write path closes the window: only the
// hash is ever persisted.
func HashAdminPasswordIfPlaintext(password string) (string, error) {
    if isBcryptHash(password) {
        return password, nil
    }
    hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
    if err != nil {
        return "", fmt.Errorf("hash admin password: %w", err)
    }
    return string(hash), nil
}

func (a *AdminAuth) Enabled() bool {
    a.mu.RLock()
    defer a.mu.RUnlock()
    return len(a.jwtSecret) > 0 && len(a.adminUsers) > 0
}

type AdminClaims struct {
    Username string `json:"username"`
    Role     string `json:"role"`
    jwt.RegisteredClaims
}

func (a *AdminAuth) GenerateToken(username, role string) (string, error) {
    a.mu.RLock()
    secret := a.jwtSecret
    a.mu.RUnlock()
    if len(secret) == 0 {
        return "", errors.New("admin JWT secret not configured")
    }
    claims := AdminClaims{
        Username: username,
        Role:     role,
        RegisteredClaims: jwt.RegisteredClaims{
            ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
            IssuedAt:  jwt.NewNumericDate(time.Now()),
            Issuer:    "fusion-gateway-admin",
        },
    }
    token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
    return token.SignedString(secret)
}

func (a *AdminAuth) ValidateToken(tokenStr string) (*AdminClaims, error) {
    a.mu.RLock()
    secret := a.jwtSecret
    a.mu.RUnlock()
    if len(secret) == 0 {
        return nil, errors.New("admin JWT secret not configured")
    }
    token, err := jwt.ParseWithClaims(tokenStr, &AdminClaims{}, func(token *jwt.Token) (interface{}, error) {
        if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, errors.New("unexpected signing method")
        }
        return secret, nil
    })
    if err != nil {
        return nil, err
    }
    claims, ok := token.Claims.(*AdminClaims)
    if !ok || !token.Valid {
        return nil, errors.New("invalid token claims")
    }
    return claims, nil
}

// #12 fix: use bcrypt comparison instead of plaintext ==
func (a *AdminAuth) Authenticate(username, password string) bool {
    a.mu.RLock()
    users := a.adminUsers
    a.mu.RUnlock()
    if len(users) == 0 {
        return false
    }
    hashed, ok := users[username]
    if !ok {
        return false
    }
    err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
    return err == nil
}
