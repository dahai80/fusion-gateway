package server

import (
    "crypto/ed25519"
    "encoding/base64"
    "encoding/json"
    "bytes"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// newTestManagedSettingsServer builds a minimal Server with a managed-settings
// signer derived from a fresh Ed25519 key, for handler-level tests. Returns the
// server + the base64 private-key seed + the matching public key so tests can
// verify signatures independently.
func newTestManagedSettingsServer(t *testing.T, payload string) (*Server, string, ed25519.PublicKey) {
    t.Helper()
    pub, priv, err := ed25519.GenerateKey(nil)
    if err != nil {
        t.Fatalf("generate ed25519 key: %v", err)
    }
    seed := priv.Seed()
    seedB64 := base64.StdEncoding.EncodeToString(seed)
    cfg := &config.ConfigSnapshot{
        Config: config.Config{
            ManagedSettings: config.ManagedSettingsConfig{
                Enabled:    true,
                PrivateKey: seedB64,
                Payload:    payload,
            },
            Auth: config.AuthConfig{
                MasterKey: "test-master-key",
                Enabled:   true,
            },
        },
    }
    s := &Server{cfg: cfg}
    s.managedSettingsSigner = newManagedSettingsSigner(cfg.Config.ManagedSettings)
    if s.managedSettingsSigner == nil {
        t.Fatal("managed-settings signer nil for enabled config")
    }
    return s, seedB64, pub
}

func TestManagedSettings_ServesSignedPayload(t *testing.T) {
    payload := `{"policy":"enterprise","max_tokens":8192}`
    s, _, pub := newTestManagedSettingsServer(t, payload)

    req := httptest.NewRequest(http.MethodGet, "/gateway/v1/managed-settings", nil)
    rec := httptest.NewRecorder()
    s.handleManagedSettings(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status=%d want 200", rec.Code)
    }
    body := rec.Body.Bytes()
    if string(body) != payload {
        t.Fatalf("body=%q want %q (must be verbatim, signature is over exact bytes)", string(body), payload)
    }
    sigHeader := rec.Header().Get("X-Fusion-Signature")
    if sigHeader == "" {
        t.Fatal("missing X-Fusion-Signature header")
    }
    parts := strings.SplitN(sigHeader, ":", 2)
    if len(parts) != 2 || parts[0] != "ed25519" {
        t.Fatalf("signature header=%q want 'ed25519:<b64>'", sigHeader)
    }
    sig, err := base64.StdEncoding.DecodeString(parts[1])
    if err != nil {
        t.Fatalf("decode signature: %v", err)
    }
    if !ed25519.Verify(pub, body, sig) {
        t.Fatal("ed25519 verify failed: signature does not match served payload + pinned pubkey")
    }
}

func TestManagedSettings_EmptyPayloadServesNull(t *testing.T) {
    s, _, pub := newTestManagedSettingsServer(t, "")

    req := httptest.NewRequest(http.MethodGet, "/gateway/v1/managed-settings", nil)
    rec := httptest.NewRecorder()
    s.handleManagedSettings(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status=%d want 200", rec.Code)
    }
    body := rec.Body.Bytes()
    if string(body) != "null" {
        t.Fatalf("empty-payload body=%q want \"null\"", string(body))
    }
    sigHeader := rec.Header().Get("X-Fusion-Signature")
    parts := strings.SplitN(sigHeader, ":", 2)
    sig, _ := base64.StdEncoding.DecodeString(parts[1])
    if !ed25519.Verify(pub, body, sig) {
        t.Fatal("ed25519 verify failed on null payload")
    }
}

func TestManagedSettings_PubkeyEndpoint(t *testing.T) {
    s, _, pub := newTestManagedSettingsServer(t, `{}`)

    req := httptest.NewRequest(http.MethodGet, "/gateway/v1/managed-settings/pubkey", nil)
    rec := httptest.NewRecorder()
    s.handleManagedSettingsPubkey(rec, req)

    if rec.Code != http.StatusOK {
        t.Fatalf("status=%d want 200", rec.Code)
    }
    var resp struct {
        Alg      string `json:"alg"`
        PubKey   string `json:"public_key"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatalf("decode pubkey envelope: %v", err)
    }
    if resp.Alg != "ed25519" {
        t.Fatalf("alg=%q want ed25519", resp.Alg)
    }
    keyBytes, err := base64.StdEncoding.DecodeString(resp.PubKey)
    if err != nil {
        t.Fatalf("decode pubkey: %v", err)
    }
    if !bytes.Equal(keyBytes, pub) {
        t.Fatal("served pubkey does not match the signing key's public key")
    }
}

func TestManagedSettings_SignerNilWhenDisabled(t *testing.T) {
    s := newManagedSettingsSigner(config.ManagedSettingsConfig{Enabled: false})
    if s != nil {
        t.Fatal("signer should be nil when disabled")
    }
    s2 := newManagedSettingsSigner(config.ManagedSettingsConfig{Enabled: true, PrivateKey: ""})
    if s2 != nil {
        t.Fatal("signer should be nil when enabled but key empty")
    }
}

func TestManagedSettings_Handler503WhenSignerNil(t *testing.T) {
    s := &Server{cfg: &config.ConfigSnapshot{Config: config.Config{}}}
    req := httptest.NewRequest(http.MethodGet, "/gateway/v1/managed-settings", nil)
    rec := httptest.NewRecorder()
    s.handleManagedSettings(rec, req)
    if rec.Code != http.StatusServiceUnavailable {
        t.Fatalf("status=%d want 503", rec.Code)
    }
}

func TestManagedSettings_MethodNotAllowed(t *testing.T) {
    s, _, _ := newTestManagedSettingsServer(t, `{}`)
    req := httptest.NewRequest(http.MethodPost, "/gateway/v1/managed-settings", nil)
    rec := httptest.NewRecorder()
    s.handleManagedSettings(rec, req)
    if rec.Code != http.StatusMethodNotAllowed {
        t.Fatalf("status=%d want 405", rec.Code)
    }
}

func TestManagedSettings_VerbatimBytesStableSignature(t *testing.T) {
    // The payload must be served as the exact configured bytes — NOT
    // re-serialized — because the signature is over the served bytes. If the
    // handler re-marshaled (e.g. via json.Marshal), key ordering could drift
    // from the configured form and the client's verification over received
    // bytes would still pass, but the operator's intent (the exact document)
    // could silently change. Assert byte-equality.
    payload := `{"b":2,"a":1}`
    s, _, pub := newTestManagedSettingsServer(t, payload)
    req := httptest.NewRequest(http.MethodGet, "/gateway/v1/managed-settings", nil)
    rec := httptest.NewRecorder()
    s.handleManagedSettings(rec, req)
    if rec.Body.String() != payload {
        t.Fatalf("key order changed: got %q want %q", rec.Body.String(), payload)
    }
    parts := strings.SplitN(rec.Header().Get("X-Fusion-Signature"), ":", 2)
    sig, _ := base64.StdEncoding.DecodeString(parts[1])
    if !ed25519.Verify(pub, rec.Body.Bytes(), sig) {
        t.Fatal("verify failed")
    }
}
