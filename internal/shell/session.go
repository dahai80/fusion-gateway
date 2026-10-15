package shell

import (
    "crypto/hmac"
    "crypto/rand"
    "crypto/sha256"
    "encoding/base64"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "log/slog"
    "strings"
    "sync"
    "time"
)

// SessionClaims ride inside the signed token (PRD §4.1): user, roles, the
// authorized module id list, and the reserved tenant_id (fixed "local" in
// the single-tenant v1).
type SessionClaims struct {
    Sub       string   `json:"sub"`
    Roles     []string `json:"roles"`
    Modules   []string `json:"permissions"`
    TenantID  string   `json:"tenant_id"`
    MustChangePassword bool `json:"must_change_password,omitempty"`
    Exp       int64    `json:"exp"`
}

// Manager issues and validates shell session tokens (HMAC-SHA256 over a
// base64url JSON payload). Separate secret space from the admin JWT and from
// API keys: a shell session can never authenticate /v1/* or /admin/* routes
// and vice versa.
type Manager struct {
    mu       sync.RWMutex
    secret   []byte
    ttl      time.Duration
    revoked  map[string]int64 // token-hash hex -> exp; logout denylist
    nowFn    func() time.Time
}

func NewManager(secret []byte, ttl time.Duration) *Manager {
    if ttl <= 0 {
        ttl = 12 * time.Hour
    }
    return &Manager{
        secret:  secret,
        ttl:     ttl,
        revoked: make(map[string]int64),
        nowFn:   time.Now,
    }
}

// Issue signs a session for the given claims, stamping the expiry.
func (m *Manager) Issue(claims SessionClaims) (string, error) {
    now := m.nowFn()
    claims.Exp = now.Add(m.ttl).Unix()
    if claims.TenantID == "" {
        claims.TenantID = "local"
    }
    payload, err := json.Marshal(claims)
    if err != nil {
        return "", fmt.Errorf("marshal session claims: %w", err)
    }
    encoded := base64.RawURLEncoding.EncodeToString(payload)
    mac := hmac.New(sha256.New, m.secret)
    mac.Write([]byte(encoded))
    sig := hex.EncodeToString(mac.Sum(nil))
    return encoded + "." + sig, nil
}

// Validate parses + verifies a token: signature, expiry, and the logout
// denylist. Returns the claims on success.
func (m *Manager) Validate(token string) (SessionClaims, error) {
    var claims SessionClaims
    encoded, sig, found := strings.Cut(token, ".")
    if !found || encoded == "" || sig == "" {
        return claims, fmt.Errorf("malformed session token")
    }
    mac := hmac.New(sha256.New, m.secret)
    mac.Write([]byte(encoded))
    expect := hex.EncodeToString(mac.Sum(nil))
    if !hmac.Equal([]byte(sig), []byte(expect)) {
        return claims, fmt.Errorf("invalid session token signature")
    }

    key := tokenHashHex(token)
    m.mu.RLock()
    _, revoked := m.revoked[key]
    m.mu.RUnlock()
    if revoked {
        return claims, fmt.Errorf("session token revoked")
    }

    raw, err := base64.RawURLEncoding.DecodeString(encoded)
    if err != nil {
        return claims, fmt.Errorf("decode session payload: %w", err)
    }
    if err := json.Unmarshal(raw, &claims); err != nil {
        return claims, fmt.Errorf("unmarshal session claims: %w", err)
    }
    if m.nowFn().Unix() >= claims.Exp {
        return claims, fmt.Errorf("session token expired")
    }
    return claims, nil
}

// Revoke adds a token to the logout denylist (PRD §4.1 logout channel).
// Entries are pruned opportunistically once past expiry so the map stays
// bounded by live sessions.
func (m *Manager) Revoke(token string) {
    claims, err := m.Validate(token)
    if err != nil {
        return
    }
    key := tokenHashHex(token)
    m.mu.Lock()
    m.revoked[key] = claims.Exp
    m.pruneLocked()
    m.mu.Unlock()
}

func (m *Manager) pruneLocked() {
    now := m.nowFn().Unix()
    for k, exp := range m.revoked {
        if now >= exp {
            delete(m.revoked, k)
        }
    }
}

func tokenHashHex(token string) string {
    sum := sha256.Sum256([]byte(token))
    return hex.EncodeToString(sum[:])
}

// GenerateBootstrapAdmin creates the first-run admin account (PRD §4.1.5):
// random 24-char password, logged once, must_change_password set in claims.
func GenerateBootstrapAdmin() (username, password string) {
    raw := make([]byte, 18)
    if _, err := rand.Read(raw); err != nil {
        // Fail loudly: a predictable bootstrap password is a credential leak.
        panic("shell: crypto/rand unavailable for bootstrap admin password: " + err.Error())
    }
    password = base64.RawURLEncoding.EncodeToString(raw)
    slog.Info("shell bootstrap admin generated (change password on first login)",
        "username", "admin",
        "password", password)
    return "admin", password
}
