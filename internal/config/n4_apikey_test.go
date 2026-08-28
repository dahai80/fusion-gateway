package config

// N4 (audit) test: backends[*].api_key stored as "enc:<base64 ciphertext>"
// must decrypt to plaintext at Load; plaintext keys load unchanged (warned);
// a malformed "enc:" value fails Load loudly so a wrong key doesn't look like
// an upstream auth bug.

import (
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/crypto"
)

// validMasterKey is a 32+ char master key for the AES-256-GCM cipher.
const n4ValidMasterKey = "0123456789abcdef0123456789abcdef-test-key-n4"

// TestN4_EncryptedAPIKeyDecrypts: a round-trip encrypt→"enc:"→decrypt yields
// the original plaintext, and the config's backends[].APIKey is the plaintext
// after decryptBackendAPIKeys.
func TestN4_EncryptedAPIKeyDecrypts(t *testing.T) {
    cipher, err := crypto.NewAESCipher(n4ValidMasterKey)
    if err != nil {
        t.Fatalf("NewAESCipher: %v", err)
    }
    plain := "sk-test-backend-secret-12345"
    ct, err := cipher.Encrypt(plain)
    if err != nil {
        t.Fatalf("Encrypt: %v", err)
    }
    cfg := &Config{
        Encryption: &EncryptionConfig{MasterKey: n4ValidMasterKey},
        Backends: map[string]BackendConfig{
            "cloud": {Type: "openai-compatible", APIKey: "enc:" + ct, Enabled: true},
        },
    }
    if err := decryptBackendAPIKeys(cfg); err != nil {
        t.Fatalf("decryptBackendAPIKeys: %v", err)
    }
    got := cfg.Backends["cloud"].APIKey
    if got != plain {
        t.Errorf("N4: decrypted api_key = %q, want %q", got, plain)
    }
}

// TestN4_PlaintextKeyPassesThrough: a plaintext api_key (no "enc:" prefix)
// loads unchanged when a master_key is set — backward-compat until rotation.
func TestN4_PlaintextKeyPassesThrough(t *testing.T) {
    cfg := &Config{
        Encryption: &EncryptionConfig{MasterKey: n4ValidMasterKey},
        Backends: map[string]BackendConfig{
            "cloud": {Type: "openai-compatible", APIKey: "sk-plaintext-still-here", Enabled: true},
        },
    }
    if err := decryptBackendAPIKeys(cfg); err != nil {
        t.Fatalf("decryptBackendAPIKeys: %v", err)
    }
    if got := cfg.Backends["cloud"].APIKey; got != "sk-plaintext-still-here" {
        t.Errorf("N4: plaintext api_key must pass through, got %q", got)
    }
}

// TestN4_NoMasterKeyPlaintextPasses: with no master_key, a plaintext key still
// loads (warned) — the gateway stays usable for users who haven't configured
// encryption yet.
func TestN4_NoMasterKeyPlaintextPasses(t *testing.T) {
    cfg := &Config{
        Encryption: nil,
        Backends: map[string]BackendConfig{
            "cloud": {Type: "openai-compatible", APIKey: "sk-no-master-config", Enabled: true},
        },
    }
    if err := decryptBackendAPIKeys(cfg); err != nil {
        t.Fatalf("decryptBackendAPIKeys: %v", err)
    }
    if got := cfg.Backends["cloud"].APIKey; got != "sk-no-master-config" {
        t.Errorf("N4: no master_key must still pass plaintext, got %q", got)
    }
}

// TestN4_MalformedEncFailsLoud: an "enc:" value that cannot decrypt with the
// configured key must return an error — never silently produce an empty key
// that would send an unauthenticated upstream request masquerading as an
// upstream bug.
func TestN4_MalformedEncFailsLoud(t *testing.T) {
    cfg := &Config{
        Encryption: &EncryptionConfig{MasterKey: n4ValidMasterKey},
        Backends: map[string]BackendConfig{
            "cloud": {Type: "openai-compatible", APIKey: "enc:not-valid-base64-ciphertext", Enabled: true},
        },
    }
    err := decryptBackendAPIKeys(cfg)
    if err == nil {
        t.Fatal("N4: malformed enc: value must error, got nil")
    }
    if !strings.Contains(err.Error(), "decrypt backends.cloud.api_key") {
        t.Errorf("N4: error must name the failing backend, got: %v", err)
    }
}

// TestN4_WrongKeyFails: ciphertext encrypted under one key must NOT decrypt
// under a different master_key — GCM auth tag must reject it loudly.
func TestN4_WrongKeyFails(t *testing.T) {
    cipher, err := crypto.NewAESCipher(n4ValidMasterKey)
    if err != nil {
        t.Fatalf("NewAESCipher: %v", err)
    }
    ct, _ := cipher.Encrypt("sk-real-secret")
    cfg := &Config{
        Encryption: &EncryptionConfig{MasterKey: "a-different-32-char-master-key-xx"},
        Backends: map[string]BackendConfig{
            "cloud": {Type: "openai-compatible", APIKey: "enc:" + ct, Enabled: true},
        },
    }
    if err := decryptBackendAPIKeys(cfg); err == nil {
        t.Fatal("N4: ciphertext under wrong master_key must fail GCM auth, got nil")
    }
}

// TestN4_EmptyKeySkipped: an empty api_key is left empty (no decrypt attempt,
// no error) — providers handle empty key per their own auth contract.
func TestN4_EmptyKeySkipped(t *testing.T) {
    cfg := &Config{
        Encryption: &EncryptionConfig{MasterKey: n4ValidMasterKey},
        Backends: map[string]BackendConfig{
            "local": {Type: "fusion-mlx", APIKey: "", Enabled: true},
        },
    }
    if err := decryptBackendAPIKeys(cfg); err != nil {
        t.Fatalf("decryptBackendAPIKeys: %v", err)
    }
    if got := cfg.Backends["local"].APIKey; got != "" {
        t.Errorf("N4: empty api_key must stay empty, got %q", got)
    }
}
