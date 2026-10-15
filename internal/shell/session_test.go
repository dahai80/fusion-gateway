package shell

import (
    "strings"
    "testing"
    "time"
)

func TestManager_IssueValidateRoundtrip(t *testing.T) {
    m := NewManager([]byte("test-secret-at-least-32-characters-long!"), time.Hour)
    token, err := m.Issue(SessionClaims{
        Sub:     "admin",
        Roles:   []string{"admin"},
        Modules: []string{"*"},
    })
    if err != nil {
        t.Fatalf("issue: %v", err)
    }
    claims, err := m.Validate(token)
    if err != nil {
        t.Fatalf("validate: %v", err)
    }
    if claims.Sub != "admin" || claims.TenantID != "local" {
        t.Fatalf("claims = %+v", claims)
    }
    if len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
        t.Fatalf("roles = %v", claims.Roles)
    }
}

func TestManager_RejectsTamperedAndWrongSecret(t *testing.T) {
    m := NewManager([]byte("test-secret-at-least-32-characters-long!"), time.Hour)
    token, _ := m.Issue(SessionClaims{Sub: "u"})
    if _, err := m.Validate(token + "x"); err == nil {
        t.Fatal("tampered token accepted")
    }
    other := NewManager([]byte("another-secret-also-32-characters-long!!"), time.Hour)
    if _, err := other.Validate(token); err == nil {
        t.Fatal("token signed by different secret accepted")
    }
    if _, err := m.Validate("garbage"); err == nil {
        t.Fatal("garbage token accepted")
    }
    if _, err := m.Validate("a.b"); err == nil {
        t.Fatal("malformed token accepted")
    }
}

func TestManager_Expiry(t *testing.T) {
    m := NewManager([]byte("test-secret-at-least-32-characters-long!"), 50*time.Millisecond)
    token, _ := m.Issue(SessionClaims{Sub: "u"})
    time.Sleep(80 * time.Millisecond)
    if _, err := m.Validate(token); err == nil || !strings.Contains(err.Error(), "expired") {
        t.Fatalf("want expiry error, got %v", err)
    }
}

func TestManager_RevokeLogout(t *testing.T) {
    m := NewManager([]byte("test-secret-at-least-32-characters-long!"), time.Hour)
    token, _ := m.Issue(SessionClaims{Sub: "u"})
    if _, err := m.Validate(token); err != nil {
        t.Fatalf("pre-revoke validate: %v", err)
    }
    m.Revoke(token)
    if _, err := m.Validate(token); err == nil || !strings.Contains(err.Error(), "revoked") {
        t.Fatalf("want revoked error, got %v", err)
    }
    // Revoking garbage is a no-op, not a panic.
    m.Revoke("garbage")
}

func TestManager_RevokedMapBounded(t *testing.T) {
    m := NewManager([]byte("test-secret-at-least-32-characters-long!"), time.Hour)
    base := time.Now()
    m.nowFn = func() time.Time { return base }
    for i := 0; i < 100; i++ {
        token, _ := m.Issue(SessionClaims{Sub: "u"})
        m.Revoke(token)
    }
    // Advance past every revocation's expiry, then revoke one more: the
    // prune drops the 100 expired entries, the fresh one survives.
    base = base.Add(2 * time.Hour)
    token, _ := m.Issue(SessionClaims{Sub: "u"})
    m.Revoke(token)
    m.mu.RLock()
    n := len(m.revoked)
    m.mu.RUnlock()
    if n != 1 {
        t.Fatalf("revoked map not pruned: %d entries", n)
    }
}

func TestGenerateBootstrapAdmin(t *testing.T) {
    u1, p1 := GenerateBootstrapAdmin()
    u2, p2 := GenerateBootstrapAdmin()
    if u1 != "admin" || u2 != "admin" {
        t.Fatalf("username = %s / %s", u1, u2)
    }
    if p1 == "" || p1 == p2 {
        t.Fatal("bootstrap password empty or repeated")
    }
    if len(p1) < 16 {
        t.Fatalf("bootstrap password too short: %d", len(p1))
    }
}
