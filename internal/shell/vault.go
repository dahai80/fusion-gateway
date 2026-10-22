package shell

import (
    "crypto/sha256"
    "fmt"
    "log/slog"
    "os"
    "path/filepath"
    "sort"
    "sync"

    "github.com/fusion-gateway/fusion-gateway/internal/crypto"
)

// AdapterVault is the encrypted credential vault behind
// /api/v1/shell/adapters (#275, migration E6): per-platform adapter
// credentials (access tokens, API secrets) stored AES-256-GCM encrypted at
// rest, keyed off the master key. Only the masked projection ever leaves the
// gateway — the list API returns credentials_masked (last 4 chars), the
// plaintext round-trips exactly once, on the PUT that stores it.
type AdapterVault struct {
    mu       sync.RWMutex
    adapters map[string]*adapterRecord
    path     string // "" = memory only (tests)
    cipher   *crypto.AESCipher
}

type adapterRecord struct {
    Platform     string            `json:"platform"`
    Credentials  map[string]string `json:"credentials"` // AES-GCM base64 per value
    RateLimitRPS float64           `json:"rate_limit_rps"`
    JitterMS     int               `json:"jitter_ms"`
    Enabled      bool              `json:"enabled"`
}

// AdapterView is the GET /api/v1/shell/adapters row: never carries plaintext.
type AdapterView struct {
    Platform          string            `json:"platform"`
    Configured        bool              `json:"configured"`
    Enabled           bool              `json:"enabled"`
    CredentialsMasked map[string]string `json:"credentials_masked"`
    RateLimitRPS      float64           `json:"rate_limit_rps"`
    JitterMS          int               `json:"jitter_ms"`
}

// NewAdapterVault derives the vault key from the master key (domain-separated
// digest, same pattern as buildShellSessionSecret — the raw master key never
// reaches the cipher directly).
func NewAdapterVault(path, masterKey string) (*AdapterVault, error) {
    sum := sha256.Sum256([]byte(masterKey + ":shell-vault-v1"))
    cipher, err := crypto.NewAESCipher(string(sum[:]))
    if err != nil {
        return nil, fmt.Errorf("adapter vault cipher: %w", err)
    }
    return &AdapterVault{
        adapters: make(map[string]*adapterRecord),
        path:     path,
        cipher:   cipher,
    }, nil
}

// Load restores the vault from the store file. A missing file is not an
// error. Values that fail to decrypt (master key rotated underneath the
// store) are surfaced as configured=false rather than fatal — the operator
// re-enters the credential via PUT.
func (v *AdapterVault) Load() error {
    if v.path == "" {
        return nil
    }
    raw, err := os.ReadFile(v.path)
    if os.IsNotExist(err) {
        return nil
    }
    if err != nil {
        return fmt.Errorf("read adapter vault store: %w", err)
    }
    var records []adapterRecord
    if err := jsonUnmarshalStrict(raw, &records); err != nil {
        return fmt.Errorf("parse adapter vault store %s: %w", v.path, err)
    }
    v.mu.Lock()
    for i := range records {
        if records[i].Platform == "" {
            slog.Warn("adapter vault entry skipped (empty platform)", "index", i)
            continue
        }
        v.adapters[records[i].Platform] = &records[i]
    }
    n := len(v.adapters)
    v.mu.Unlock()
    slog.Info("adapter vault loaded", "path", v.path, "adapters", n)
    return nil
}

// Save persists the vault atomically (temp file + rename, 0600). Credential
// values are already ciphertext in memory, so the file never holds plaintext.
func (v *AdapterVault) Save() error {
    if v.path == "" {
        return nil
    }
    v.mu.RLock()
    records := make([]adapterRecord, 0, len(v.adapters))
    for _, a := range v.adapters {
        records = append(records, *a)
    }
    v.mu.RUnlock()
    sort.Slice(records, func(i, j int) bool { return records[i].Platform < records[j].Platform })
    if err := os.MkdirAll(filepath.Dir(v.path), 0o755); err != nil {
        return fmt.Errorf("create adapter vault dir: %w", err)
    }
    raw, err := jsonMarshalIndent(records)
    if err != nil {
        return fmt.Errorf("marshal adapter vault: %w", err)
    }
    tmp := v.path + ".tmp"
    if err := os.WriteFile(tmp, raw, 0o600); err != nil {
        return fmt.Errorf("write adapter vault temp: %w", err)
    }
    if err := os.Rename(tmp, v.path); err != nil {
        return fmt.Errorf("rename adapter vault store: %w", err)
    }
    return nil
}

// Put stores one platform's adapter config. Credentials are encrypted value-
// by-value; an empty credentials map keeps any existing credentials (partial
// updates from a form that only edits rate limits must not wipe secrets).
func (v *AdapterVault) Put(platform string, credentials map[string]string, rateLimitRPS float64, jitterMS int, enabled bool) error {
    if platform == "" {
        return fmt.Errorf("platform is required")
    }
    enc := make(map[string]string, len(credentials))
    for k, val := range credentials {
        if val == "" {
            continue
        }
        ct, err := v.cipher.Encrypt(val)
        if err != nil {
            return fmt.Errorf("encrypt credential %q: %w", k, err)
        }
        enc[k] = ct
    }
    v.mu.Lock()
    prev, exists := v.adapters[platform]
    if len(enc) == 0 && exists {
        enc = prev.Credentials
    }
    v.adapters[platform] = &adapterRecord{
        Platform:     platform,
        Credentials:  enc,
        RateLimitRPS: rateLimitRPS,
        JitterMS:     jitterMS,
        Enabled:      enabled,
    }
    v.mu.Unlock()
    if err := v.Save(); err != nil {
        slog.Error("adapter vault save failed", "path", v.path, "error", err)
        return err
    }
    slog.Info("adapter vault entry saved", "platform", platform, "enabled", enabled)
    return nil
}

// List returns the masked projection, sorted by platform. configured=false
// when a stored value no longer decrypts (stale master key).
func (v *AdapterVault) List() []AdapterView {
    v.mu.RLock()
    defer v.mu.RUnlock()
    views := make([]AdapterView, 0, len(v.adapters))
    for _, a := range v.adapters {
        masked := make(map[string]string, len(a.Credentials))
        configured := true
        for k, ct := range a.Credentials {
            pt, err := v.cipher.Decrypt(ct)
            if err != nil {
                configured = false
                masked[k] = "…????"
                continue
            }
            masked[k] = maskCredential(pt)
        }
        views = append(views, AdapterView{
            Platform:          a.Platform,
            Configured:        configured && len(a.Credentials) > 0,
            Enabled:           a.Enabled,
            CredentialsMasked: masked,
            RateLimitRPS:      a.RateLimitRPS,
            JitterMS:          a.JitterMS,
        })
    }
    sort.Slice(views, func(i, j int) bool { return views[i].Platform < views[j].Platform })
    return views
}

// Test validates one platform's stored credentials: every stored value must
// decrypt under the current master key. ok=false reason explains the failure.
// A real end-to-end platform probe is out of gateway scope (the adapter
// itself lives in the module process); this is the honest decrypt + shape
// check the shell Settings page renders.
func (v *AdapterVault) Test(platform string) (bool, string) {
    v.mu.RLock()
    a, exists := v.adapters[platform]
    v.mu.RUnlock()
    if !exists {
        return false, "adapter not configured"
    }
    if !a.Enabled {
        return false, "adapter disabled"
    }
    if len(a.Credentials) == 0 {
        return false, "no credentials stored"
    }
    for k, ct := range a.Credentials {
        if _, err := v.cipher.Decrypt(ct); err != nil {
            return false, fmt.Sprintf("credential %q fails to decrypt (master key changed? re-enter it)", k)
        }
    }
    return true, ""
}

// Resolve returns the decrypted credentials for one platform — the connector
// credential_ref path (#275): connector documents reference vault entries by
// platform id and never inline a secret.
func (v *AdapterVault) Resolve(platform string) (map[string]string, bool) {
    v.mu.RLock()
    a, exists := v.adapters[platform]
    v.mu.RUnlock()
    if !exists {
        return nil, false
    }
    out := make(map[string]string, len(a.Credentials))
    for k, ct := range a.Credentials {
        pt, err := v.cipher.Decrypt(ct)
        if err != nil {
            return nil, false
        }
        out[k] = pt
    }
    return out, true
}

// maskCredential keeps the last 4 chars for operator recognition; anything
// shorter masks entirely.
func maskCredential(plaintext string) string {
    if len(plaintext) <= 4 {
        return "…****"
    }
    return "…" + plaintext[len(plaintext)-4:]
}
