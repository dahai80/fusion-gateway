package shell

import (
    "errors"
    "path/filepath"
    "strings"
    "testing"
    "time"
)

func hashForTest(plaintext string) string {
    return serviceKeyHashOf(plaintext)
}

// #261 K1: issue-once semantics — first EnsureServiceKey issues, subsequent
// calls retain the existing key and never re-reveal the plaintext.
func TestServiceKey_IssueOnce(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-demo")
    m.Upstream = "http://127.0.0.1:19000"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    keyID, plaintext, issued, err := r.EnsureServiceKey("fusion-demo")
    if err != nil || !issued {
        t.Fatalf("first ensure: issued=%v err=%v", issued, err)
    }
    if !strings.HasPrefix(plaintext, "fm-fusion-demo-") {
        t.Fatalf("plaintext %q must use fm-<module>-<hex> shape", plaintext)
    }
    if keyID == "" {
        t.Fatal("key id must be non-empty")
    }
    // Proxy injection path carries the plaintext.
    if _, key, ok := r.UpstreamFor("fusion-demo"); !ok || key != plaintext {
        t.Fatalf("UpstreamFor must carry the issued plaintext, got %q ok=%v", key, ok)
    }
    keyID2, plaintext2, issued2, err := r.EnsureServiceKey("fusion-demo")
    if err != nil || issued2 || plaintext2 != "" || keyID2 != keyID {
        t.Fatalf("re-ensure must retain without re-reveal: issued=%v plaintext=%q keyID=%q err=%v", issued2, plaintext2, keyID2, err)
    }
    // List never carries the key material.
    for _, v := range r.List() {
        if v.APIKey != "" {
            t.Fatalf("List leaked APIKey: %+v", v.APIKey)
        }
    }
}

// #261 K1: heartbeat auth — current key valid, wrong key refused.
func TestServiceKey_Auth(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    if err := r.Upsert(validManifest("fusion-demo")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    _, plaintext, _, err := r.EnsureServiceKey("fusion-demo")
    if err != nil {
        t.Fatalf("ensure: %v", err)
    }
    if !r.AuthServiceKey("fusion-demo", hashForTest(plaintext), time.Hour) {
        t.Fatal("current key must authenticate")
    }
    if r.AuthServiceKey("fusion-demo", hashForTest("fm-fusion-demo-deadbeef"), time.Hour) {
        t.Fatal("wrong key must be refused")
    }
    if r.AuthServiceKey("fusion-ghost", hashForTest(plaintext), time.Hour) {
        t.Fatal("key bound to one module must not authenticate another")
    }
}

// #261 K1: rotation dual-active grace — old key valid inside the window,
// retired early by the first new-key heartbeat, refused after expiry.
func TestServiceKey_RotateGrace(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    if err := r.Upsert(validManifest("fusion-demo")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    _, oldKey, _, err := r.EnsureServiceKey("fusion-demo")
    if err != nil {
        t.Fatalf("ensure: %v", err)
    }
    _, newKey, err := r.RotateServiceKey("fusion-demo", time.Hour)
    if err != nil {
        t.Fatalf("rotate: %v", err)
    }
    if newKey == oldKey {
        t.Fatal("rotation must produce a different key")
    }
    if !r.AuthServiceKey("fusion-demo", hashForTest(oldKey), time.Hour) {
        t.Fatal("old key must stay valid inside the grace window")
    }
    if !r.AuthServiceKey("fusion-demo", hashForTest(newKey), time.Hour) {
        t.Fatal("new key must be valid")
    }
    // Early retirement: heartbeat with the new key kills the old one even
    // inside the window.
    if !r.AuthServiceKey("fusion-demo", hashForTest(newKey), time.Hour) {
        t.Fatal("new key must remain valid")
    }
    if r.AuthServiceKey("fusion-demo", hashForTest(oldKey), time.Hour) {
        t.Fatal("old key must be retired after the module migrated")
    }
    // Expired grace: rotating again, the expired old key is refused.
    _, newKey2, err := r.RotateServiceKey("fusion-demo", -time.Minute)
    if err != nil {
        t.Fatalf("rotate 2: %v", err)
    }
    if r.AuthServiceKey("fusion-demo", hashForTest(newKey), -time.Minute) {
        t.Fatal("previous key must be refused once the grace window elapsed")
    }
    if !r.AuthServiceKey("fusion-demo", hashForTest(newKey2), -time.Minute) {
        t.Fatal("current key must stay valid regardless of grace")
    }
}

// #261 K1: revocation — key material cleared, auth refused, proxy sees the
// revoked flag, re-registration (without api_key) does NOT resurrect the key.
func TestServiceKey_Revoke(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    if err := r.Upsert(validManifest("fusion-demo")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    _, plaintext, _, err := r.EnsureServiceKey("fusion-demo")
    if err != nil {
        t.Fatalf("ensure: %v", err)
    }
    if err := r.RevokeServiceKey("fusion-demo"); err != nil {
        t.Fatalf("revoke: %v", err)
    }
    if !r.IsServiceKeyRevoked("fusion-demo") {
        t.Fatal("revoked flag must be set")
    }
    if r.AuthServiceKey("fusion-demo", hashForTest(plaintext), time.Hour) {
        t.Fatal("revoked key must not authenticate")
    }
    if _, key, ok := r.UpstreamFor("fusion-demo"); ok && key != "" {
        t.Fatalf("revoked entry must not carry an upstream credential, got %q", key)
    }
    // Re-registration without api_key keeps the revoked state (module must
    // re-register through issuance — the admin rotates after re-register).
    if err := r.Upsert(validManifest("fusion-demo")); err != nil {
        t.Fatalf("re-upsert: %v", err)
    }
    if !r.IsServiceKeyRevoked("fusion-demo") {
        t.Fatal("re-registration must not clear the revoked flag")
    }
    _, _, issued, err := r.EnsureServiceKey("fusion-demo")
    if err != nil || !issued {
        t.Fatalf("ensure after revoked re-registration must re-issue: issued=%v err=%v", issued, err)
    }
}

// #261 K1: builtin manifests are gateway-owned — rotate/revoke rejected.
func TestServiceKey_BuiltinGuard(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    if err := r.Upsert(validManifest("fusion-admin")); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    r.MarkBuiltin("fusion-admin")
    if _, _, err := r.RotateServiceKey("fusion-admin", time.Hour); !errors.Is(err, ErrManifestStatic) {
        t.Fatalf("rotate builtin: want ErrManifestStatic, got %v", err)
    }
    if err := r.RevokeServiceKey("fusion-admin"); !errors.Is(err, ErrManifestStatic) {
        t.Fatalf("revoke builtin: want ErrManifestStatic, got %v", err)
    }
}

// #261 K1: module-supplied api_key path — no issuance, no key to rotate.
func TestServiceKey_ModuleSuppliedKeyPath(t *testing.T) {
    r := NewRegistry(nil, nil, 30*time.Second)
    m := validManifest("fusion-demo")
    m.Upstream = "http://127.0.0.1:19000"
    m.APIKey = "module-own-secret"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    if _, _, err := r.RotateServiceKey("fusion-demo", time.Hour); !errors.Is(err, ErrNoServiceKey) {
        t.Fatalf("rotate without issued key: want ErrNoServiceKey, got %v", err)
    }
}

// #261 K1: service key state survives the persistence round-trip (#230
// channel) — a restarted gateway keeps authenticating the module's fm- key.
func TestServiceKey_PersistRoundTrip(t *testing.T) {
    path := filepath.Join(t.TempDir(), "shell_manifests.json")
    r := NewRegistry(nil, nil, 30*time.Second)
    r.SetPersistence(path)
    m := validManifest("fusion-demo")
    m.Upstream = "http://127.0.0.1:19000"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert: %v", err)
    }
    _, plaintext, _, err := r.EnsureServiceKey("fusion-demo")
    if err != nil {
        t.Fatalf("ensure: %v", err)
    }
    r2 := NewRegistry(nil, nil, 30*time.Second)
    r2.SetPersistence(path)
    if err := r2.Load(); err != nil {
        t.Fatalf("load: %v", err)
    }
    if !r2.AuthServiceKey("fusion-demo", hashForTest(plaintext), time.Hour) {
        t.Fatal("issued key must survive the persistence round-trip")
    }
    if _, key, ok := r2.UpstreamFor("fusion-demo"); !ok || key != plaintext {
        t.Fatalf("persisted plaintext credential must round-trip for proxy injection, got %q ok=%v", key, ok)
    }
}
