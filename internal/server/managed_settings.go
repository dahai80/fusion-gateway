package server

import (
    "crypto/ed25519"
    "encoding/base64"
    "encoding/json"
    "log/slog"
    "net/http"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

// managedSettingsSigningAlgo is the signature algorithm advertised in the
// X-Fusion-Signature header ("<algo>:<base64-sig>"). Pinned to ed25519 so the
// client's verifier can switch on the algo tag and reject anything else before
// attempting verification (forward-compat: a future alg change ships a new tag
// and the client opts in, no silent breakage).
const managedSettingsSigningAlgo = "ed25519"

// managedSettingsSigner holds the derived Ed25519 key pair for signing
// managed-settings payloads. Built once from config at server start (and
// rebuilt on hot-reload via the ConfigSnapshot read path — see
// newManagedSettingsSigner). The private key signs; the public key is exposed
// at /gateway/v1/managed-settings/pubkey for client pinning.
type managedSettingsSigner struct {
    priv ed25519.PrivateKey
    pub  ed25519.PublicKey
}

// newManagedSettingsSigner derives the Ed25519 key pair from the base64 seed in
// config. Returns nil when managed_settings is disabled or the key is absent —
// the endpoint is not registered in that case (see server.go), so a nil signer
// is never reached by a handler. Validation that the seed decodes to 32 bytes
// happens in config.validate (fail-closed at load time), so by the time a
// signer is built the key is well-formed; a defensive decode error here logs +
// returns nil rather than panicking.
func newManagedSettingsSigner(cfg config.ManagedSettingsConfig) *managedSettingsSigner {
    if !cfg.Enabled || cfg.PrivateKey == "" {
        return nil
    }
    seed, err := base64.StdEncoding.DecodeString(cfg.PrivateKey)
    if err != nil {
        slog.Error("managed_settings: private_key base64 decode failed at signer build", "error", err)
        return nil
    }
    if len(seed) != ed25519.SeedSize {
        slog.Error("managed_settings: private_key wrong seed length at signer build", "got", len(seed), "want", ed25519.SeedSize)
        return nil
    }
    priv := ed25519.NewKeyFromSeed(seed)
    pub := priv.Public().(ed25519.PublicKey)
    return &managedSettingsSigner{priv: priv, pub: pub}
}

// publicKeyBase64 returns the base64-encoded public key for the pubkey
// endpoint. Clients pin this value and verify payloads against it.
func (s *managedSettingsSigner) publicKeyBase64() string {
    return base64.StdEncoding.EncodeToString(s.pub)
}

// sign returns a detached Ed25519 signature over the raw payload bytes.
func (s *managedSettingsSigner) sign(payload []byte) []byte {
    return ed25519.Sign(s.priv, payload)
}

// handleManagedSettings serves the signed managed-settings payload to
// enterprise fusion-code clients (GET /gateway/v1/managed-settings). The raw
// configured payload bytes are served verbatim (NOT re-serialized) so the
// signature is stable against JSON key-order drift — the client verifies the
// signature over exactly the bytes it receives. A detached signature is sent
// in the X-Fusion-Signature header ("ed25519:<base64>"); the client verifies
// against a pinned public key (fetched once from the pubkey endpoint) before
// trusting the payload. An empty configured payload serves a literal "null"
// JSON document (still signed) so the client always gets a verifiable
// response. Behind withMiddleware (fg-key auth) — enterprise clients
// authenticate with their API key.
func (s *Server) handleManagedSettings(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    signer := s.managedSettingsSigner
    if signer == nil {
        slog.Warn("managed_settings: endpoint reached but signer not configured")
        http.Error(w, `{"error":{"message":"managed-settings signing not configured","type":"disabled"}}`, http.StatusServiceUnavailable)
        return
    }
    payload := []byte(s.cfg.Config.ManagedSettings.Payload)
    if len(payload) == 0 {
        payload = []byte("null")
    }
    sig := signer.sign(payload)
    w.Header().Set("Content-Type", "application/json")
    w.Header().Set("X-Fusion-Signature", managedSettingsSigningAlgo+":"+base64.StdEncoding.EncodeToString(sig))
    w.WriteHeader(http.StatusOK)
    w.Write(payload)
    slog.Info("managed_settings served", "bytes", len(payload), "remote", r.RemoteAddr)
}

// handleManagedSettingsPubkey exposes the Ed25519 public key for client
// pinning (GET /gateway/v1/managed-settings/pubkey). Returns a small JSON
// envelope {"alg":"ed25519","public_key":"<base64>"} so a client can fetch +
// pin the key out-of-band (operator distributes the pinned value via deploy
// config, then the client refuses payloads not signed by the matching
// private key). Behind withMiddleware (fg-key auth) — same surface as the
// payload endpoint.
func (s *Server) handleManagedSettingsPubkey(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet {
        http.Error(w, `{"error":{"message":"Method not allowed","type":"invalid_request"}}`, http.StatusMethodNotAllowed)
        return
    }
    signer := s.managedSettingsSigner
    if signer == nil {
        slog.Warn("managed_settings: pubkey endpoint reached but signer not configured")
        http.Error(w, `{"error":{"message":"managed-settings signing not configured","type":"disabled"}}`, http.StatusServiceUnavailable)
        return
    }
    resp := map[string]string{
        "alg":       managedSettingsSigningAlgo,
        "public_key": signer.publicKeyBase64(),
    }
    body, err := json.Marshal(resp)
    if err != nil {
        slog.Error("managed_settings: pubkey marshal failed", "error", err)
        http.Error(w, `{"error":{"message":"Internal error","type":"internal"}}`, http.StatusInternalServerError)
        return
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusOK)
    w.Write(body)
}
