package exchange

// Issue #595: short-lived scoped token plane. The gateway mints RS256 JWTs
// from a bootstrap credential (static/store API key, or a shell session
// token); the minted token is the ONLY thing a client-side or sub-app
// integration needs to hold, and it dies at exp (<= max TTL). The signing
// keypair is generated on first boot and persisted in the data dir so the
// JWKS (and every previously minted token) survives restarts.

import (
    "crypto/rand"
    "crypto/rsa"
    "crypto/sha256"
    "crypto/x509"
    "encoding/base64"
    "encoding/hex"
    "encoding/pem"
    "errors"
    "fmt"
    "log/slog"
    "math/big"
    "os"
    "path/filepath"
    "sync"
    "time"

    "github.com/golang-jwt/jwt/v5"
)

const (
    signingKeyFile = "token_signing_key.pem"
    rsKeyBits      = 2048
    issuer         = "fusion-gateway"
)

// Claims is the minted token payload. The enforcement plane (/v1/*) reads
// scope from these claims instead of trusting a full API key: the model
// allowlist, rate caps, and tenant are exactly the minting key's, frozen at
// exchange time.
type Claims struct {
    jwt.RegisteredClaims
    Tenant string   `json:"tenant,omitempty"`
    Models []string `json:"models,omitempty"`
    RPM    int      `json:"rpm,omitempty"`
    TPM    int      `json:"tpm,omitempty"`
}

type Signer struct {
    mu      sync.Mutex
    key     *rsa.PrivateKey
    kid     string
    dataDir string
}

// NewSigner loads the persisted signing key from dataDir, generating and
// writing one (0600) when absent. A missing dataDir or unwritable path is an
// error: the exchange plane must not silently fall back to a per-boot key
// (every restart would invalidate outstanding tokens).
func NewSigner(dataDir string) (*Signer, error) {
    if dataDir == "" {
        return nil, errors.New("exchange: data_dir is empty; cannot persist signing key")
    }
    path := filepath.Join(dataDir, signingKeyFile)
    if data, err := os.ReadFile(path); err == nil {
        key, err := parsePKCS1(data)
        if err != nil {
            return nil, fmt.Errorf("exchange: corrupt signing key %s: %w", path, err)
        }
        return newSignerWithKey(key, dataDir), nil
    } else if !errors.Is(err, os.ErrNotExist) {
        return nil, fmt.Errorf("exchange: read signing key %s: %w", path, err)
    }
    key, err := rsa.GenerateKey(rand.Reader, rsKeyBits)
    if err != nil {
        return nil, fmt.Errorf("exchange: generate signing key: %w", err)
    }
    der := x509.MarshalPKCS1PrivateKey(key)
    if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), 0600); err != nil {
        return nil, fmt.Errorf("exchange: persist signing key %s: %w", path, err)
    }
    slog.Info("exchange: generated new token signing key", "path", path)
    return newSignerWithKey(key, dataDir), nil
}

func parsePKCS1(data []byte) (*rsa.PrivateKey, error) {
    block, _ := pem.Decode(data)
    if block == nil {
        return nil, errors.New("no PEM block")
    }
    return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func newSignerWithKey(key *rsa.PrivateKey, dataDir string) *Signer {
    sum := sha256.Sum256(key.N.Bytes())
    return &Signer{key: key, kid: hex.EncodeToString(sum[:4]), dataDir: dataDir}
}

// Sign mints a token. exp is capped by the caller (min of requested TTL,
// configured cap, bootstrap credential lifetime).
func (s *Signer) Sign(sub, audience string, ttl time.Duration, tenant string, models []string, rpm, tpm int) (string, time.Time, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    now := time.Now()
    exp := now.Add(ttl)
    jtiBytes := make([]byte, 12)
    if _, err := rand.Read(jtiBytes); err != nil {
        return "", time.Time{}, fmt.Errorf("exchange: jti: %w", err)
    }
    claims := Claims{
        RegisteredClaims: jwt.RegisteredClaims{
            Issuer:    issuer,
            Subject:   sub,
            Audience:  jwt.ClaimStrings{audience},
            ExpiresAt: jwt.NewNumericDate(exp),
            IssuedAt:  jwt.NewNumericDate(now),
            ID:        hex.EncodeToString(jtiBytes),
        },
        Tenant: tenant,
        Models: models,
        RPM:    rpm,
        TPM:    tpm,
    }
    token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
    token.Header["kid"] = s.kid
    signed, err := token.SignedString(s.key)
    if err != nil {
        return "", time.Time{}, fmt.Errorf("exchange: sign: %w", err)
    }
    return signed, exp, nil
}

// Verify parses and validates a minted token: RS256 only (alg confusion
// rejected), matching kid, unexpired. Signature checked against the local
// public key — no external fetch.
func (s *Signer) Verify(tokenStr string) (*Claims, error) {
    s.mu.Lock()
    pub := &s.key.PublicKey
    kid := s.kid
    s.mu.Unlock()
    token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
        if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
            return nil, fmt.Errorf("unexpected signing method %v (alg confusion rejected)", t.Header["alg"])
        }
        if k, ok := t.Header["kid"].(string); ok && k != kid {
            return nil, fmt.Errorf("unknown kid %q", k)
        }
        return pub, nil
    }, jwt.WithExpirationRequired())
    if err != nil {
        return nil, fmt.Errorf("exchange: invalid token: %w", err)
    }
    claims, ok := token.Claims.(*Claims)
    if !ok || !token.Valid {
        return nil, errors.New("exchange: invalid token claims")
    }
    return claims, nil
}

// JWKS renders the public key as a JWK set (RFC 7517) so downstream
// verifiers can validate minted tokens without trusting the gateway
// response body. Single-key set; kid matches the token header.
func (s *Signer) JWKS() map[string]any {
    s.mu.Lock()
    pub := s.key.PublicKey
    kid := s.kid
    s.mu.Unlock()
    e := big.NewInt(int64(pub.E))
    return map[string]any{
        "keys": []map[string]any{{
            "kty": "RSA",
            "use": "sig",
            "alg": "RS256",
            "kid": kid,
            "n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
            "e":   base64.RawURLEncoding.EncodeToString(e.Bytes()),
        }},
    }
}
