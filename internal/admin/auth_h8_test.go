package admin

// H8 guard tests: the admin password write path must hash the password BEFORE
// persisting (never plaintext on disk), and a rotation must take effect on the
// live AdminAuth immediately (no restart required). Revert HashAdminPasswordIfPlaintext
// (return password as-is) → TestH8_PasswordHashedNotPlaintext fails. Revert
// ReloadUsers (no-op) → TestH8_RotationImmediateNoRestart fails.

import (
    "strings"
    "testing"
)

// TestH8_PasswordHashedNotPlaintext: HashAdminPasswordIfPlaintext must return a
// bcrypt hash, never the raw password. The write path calls this before
// persisting to config.yaml; if it returned plaintext, config.yaml would store
// the raw admin password until the next startup bcrypt pass (audit H8).
func TestH8_PasswordHashedNotPlaintext(t *testing.T) {
    raw := "supersecret-password-123"
    hashed, err := HashAdminPasswordIfPlaintext(raw)
    if err != nil {
        t.Fatalf("H8: hashing failed: %v", err)
    }
    if hashed == raw {
        t.Fatalf("H8: HashAdminPasswordIfPlaintext returned the raw password — config.yaml would store plaintext")
    }
    if !isBcryptHash(hashed) {
        t.Fatalf("H8: hashed password must be a bcrypt hash ($2a$/$2b$/$2y$, 60 chars), got %q", hashed)
    }
    // The hash must verify against the raw password.
    auth := &AdminAuth{adminUsers: map[string]string{"admin": hashed}}
    if !auth.Authenticate("admin", raw) {
        t.Fatalf("H8: hashed password does not verify against the raw password (hash corrupt)")
    }
    // Wrong password must NOT verify.
    if auth.Authenticate("admin", "wrong-password-456") {
        t.Fatalf("H8: wrong password incorrectly verified (hash broken)")
    }
}

// TestH8_HashIdempotentOnBcrypt: feeding an already-hashed password through
// HashAdminPasswordIfPlaintext returns it unchanged (no double-hash).
func TestH8_HashIdempotentOnBcrypt(t *testing.T) {
    raw := "supersecret-password-123"
    hashed, _ := HashAdminPasswordIfPlaintext(raw)
    rehashed, err := HashAdminPasswordIfPlaintext(hashed)
    if err != nil {
        t.Fatalf("H8: re-hashing a bcrypt hash failed: %v", err)
    }
    if rehashed != hashed {
        t.Fatalf("H8: re-hashing a bcrypt hash must be idempotent, got %q then %q", hashed, rehashed)
    }
}

// TestH8_RotationImmediateNoRestart: ReloadUsers must make a rotated password
// authenticate immediately, without reconstructing AdminAuth (no restart).
// Before H8, NewAdminAuth was called once at server.New; a password set via the
// admin API did not take effect until the next process restart.
func TestH8_RotationImmediateNoRestart(t *testing.T) {
    auth, err := NewAdminAuth("jwt-secret-at-least-32-chars-long!!", map[string]string{"admin": "old-password-12345"})
    if err != nil {
        t.Fatalf("H8: NewAdminAuth failed: %v", err)
    }
    if !auth.Authenticate("admin", "old-password-12345") {
        t.Fatalf("H8: initial password must authenticate")
    }
    // Rotate: persist a new (already-hashed) password, then reload.
    newHashed, _ := HashAdminPasswordIfPlaintext("new-password-67890")
    auth.ReloadUsers(map[string]string{"admin": newHashed})
    // Old password must now be rejected.
    if auth.Authenticate("admin", "old-password-12345") {
        t.Fatalf("H8: old password still authenticates after ReloadUsers — rotation did not apply live (still needs restart)")
    }
    // New password must authenticate immediately, no restart.
    if !auth.Authenticate("admin", "new-password-67890") {
        t.Fatalf("H8: new password does not authenticate immediately after ReloadUsers (rotation needs restart — H8 not fixed)")
    }
}

// TestH8_SecretRotationImmediate: ReloadSecret must rotate the signing key live;
// a token signed with the old secret must fail validation, the new secret signs.
func TestH8_SecretRotationImmediate(t *testing.T) {
    auth, err := NewAdminAuth("jwt-secret-at-least-32-chars-long!!", map[string]string{"admin": "pw-12345678"})
    if err != nil {
        t.Fatalf("H8: NewAdminAuth failed: %v", err)
    }
    oldToken, _ := auth.GenerateToken("admin", "admin")
    auth.ReloadSecret("new-jwt-secret-at-least-32-chars-long!")
    // Old token (signed with old secret) must fail under the new secret.
    if _, err := auth.ValidateToken(oldToken); err == nil {
        t.Fatalf("H8: token signed with old secret still validates after ReloadSecret — secret rotation did not apply live")
    }
    // New token signs + validates under the new secret.
    newToken, err := auth.GenerateToken("admin", "admin")
    if err != nil {
        t.Fatalf("H8: GenerateToken failed after ReloadSecret: %v", err)
    }
    if _, err := auth.ValidateToken(newToken); err != nil {
        t.Fatalf("H8: new token does not validate after ReloadSecret: %v", err)
    }
}

// TestH8_HashDoesNotLeakPlaintext: the helper must never embed the raw password
// in the output (paranoia: a buggy hash scheme could suffix it).
func TestH8_HashDoesNotLeakPlaintext(t *testing.T) {
    raw := "leak-check-password-999"
    hashed, _ := HashAdminPasswordIfPlaintext(raw)
    if strings.Contains(hashed, raw) {
        t.Fatalf("H8: hashed output contains the raw plaintext password — leak")
    }
}
