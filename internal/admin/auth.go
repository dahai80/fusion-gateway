package admin

import (
    "errors"
    "fmt"
    "log/slog"
    "time"

    "github.com/golang-jwt/jwt/v5"
    "golang.org/x/crypto/bcrypt"
)

// A1 fix: AdminAuth replaces global jwtSecret + adminUsers variables.
// Constructed via NewAdminAuth, injected into Server and Handler.
// #12 fix: passwords stored as bcrypt hashes, not plaintext.
type AdminAuth struct {
    jwtSecret      []byte
    adminUsers     map[string]string // username -> bcrypt hash
    insecureCookie bool              // true when running without TLS
}

func (a *AdminAuth) SetInsecureCookie(v bool) {
    a.insecureCookie = v
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
        jwtSecret:  []byte(secret),
        adminUsers: hashedUsers,
    }, nil
}

// isBcryptHash checks if a string looks like a pre-computed bcrypt hash ($2a$, $2b$, $2y$)
func isBcryptHash(s string) bool {
    return len(s) == 60 && (s[:4] == "$2a$" || s[:4] == "$2b$" || s[:4] == "$2y$")
}

func (a *AdminAuth) Enabled() bool {
    return len(a.jwtSecret) > 0 && len(a.adminUsers) > 0
}

type AdminClaims struct {
    Username string `json:"username"`
    Role     string `json:"role"`
    jwt.RegisteredClaims
}

func (a *AdminAuth) GenerateToken(username, role string) (string, error) {
    if len(a.jwtSecret) == 0 {
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
    return token.SignedString(a.jwtSecret)
}

func (a *AdminAuth) ValidateToken(tokenStr string) (*AdminClaims, error) {
    if len(a.jwtSecret) == 0 {
        return nil, errors.New("admin JWT secret not configured")
    }
    token, err := jwt.ParseWithClaims(tokenStr, &AdminClaims{}, func(token *jwt.Token) (interface{}, error) {
        if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, errors.New("unexpected signing method")
        }
        return a.jwtSecret, nil
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
    if len(a.adminUsers) == 0 {
        return false
    }
    hashed, ok := a.adminUsers[username]
    if !ok {
        return false
    }
    err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
    return err == nil
}
