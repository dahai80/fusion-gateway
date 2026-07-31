package admin

import (
    "errors"
    "fmt"
    "log/slog"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

var jwtSecret []byte

func SetJWTSecret(secret string) {
    if secret == "" {
        slog.Error("admin JWT secret is empty, admin module will be disabled")
        jwtSecret = nil
        return
    }
    if len(secret) < 32 {
        panic(fmt.Sprintf("admin JWT secret must be at least 32 characters, got %d", len(secret)))
    }
    jwtSecret = []byte(secret)
}

func JWTSecretSet() bool {
    return len(jwtSecret) > 0
}

type AdminClaims struct {
    Username string `json:"username"`
    Role     string `json:"role"`
    jwt.RegisteredClaims
}

func GenerateToken(username, role string) (string, error) {
    if len(jwtSecret) == 0 {
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
    return token.SignedString(jwtSecret)
}

func ValidateToken(tokenStr string) (*AdminClaims, error) {
    if len(jwtSecret) == 0 {
        return nil, errors.New("admin JWT secret not configured")
    }
    token, err := jwt.ParseWithClaims(tokenStr, &AdminClaims{}, func(token *jwt.Token) (interface{}, error) {
        if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
            return nil, errors.New("unexpected signing method")
        }
        return jwtSecret, nil
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
