package shell

// K1 (#261): gateway-issued per-module service keys. Direction reversed from
// the historical flow — the module no longer supplies its upstream
// credential; the gateway generates an fm-<module>-<hex> key at registration
// (when the body omits api_key), returns the plaintext exactly once, and the
// module configures its own auth to accept it. The proxy injects the same
// plaintext as the upstream Bearer credential (existing manifest.APIKey
// path); heartbeat re-auth hashes the presented key against the entry's
// stored SHA-256, so the module never needs the long-lived admin key again.

import (
    "crypto/rand"
    "crypto/sha256"
    "encoding/hex"
    "errors"
    "fmt"
    "log/slog"
    "strings"
    "time"
)

// ServiceKeyFormat is the plaintext shape handed to modules: fm-<module-id>-
// <32 hex chars>. The fm- prefix distinguishes gateway-issued module service
// keys from sk- admin keys and fk- module-local keys on sight.
const (
    serviceKeyPrefix   = "fm-"
    serviceKeyRandLen  = 16 // bytes -> 32 hex chars
    serviceKeyIDHexLen = 16
)

// ErrNoServiceKey: the dynamic registration carries no gateway-issued key
// (registered with a module-supplied api_key, or already revoked) so there is
// nothing to rotate.
var ErrNoServiceKey = errors.New("module has no gateway-issued service key")

// generateServiceKey returns the plaintext fm- key and its key id (first 16
// hex chars of the SHA-256 — stable, non-secret identifier for audit logs).
func generateServiceKey(moduleID string) (plaintext, keyID string, err error) {
    raw := make([]byte, serviceKeyRandLen)
    if _, err := rand.Read(raw); err != nil {
        return "", "", fmt.Errorf("service key entropy: %w", err)
    }
    plaintext = serviceKeyPrefix + moduleID + "-" + hex.EncodeToString(raw)
    return plaintext, serviceKeyHashOf(plaintext)[:serviceKeyIDHexLen], nil
}

func serviceKeyHashOf(plaintext string) string {
    sum := sha256.Sum256([]byte(plaintext))
    return hex.EncodeToString(sum[:])
}

// EnsureServiceKey issues a service key for a dynamic registration when it
// has none, or reports the existing key id without ever re-revealing the
// plaintext (issued=false). The plaintext is returned exactly once, at
// issuance. Caller persists via the registry's own save (done here).
func (r *Registry) EnsureServiceKey(id string) (keyID, plaintext string, issued bool, err error) {
    r.mu.Lock()
    e, ok := r.dynamic[id]
    if !ok {
        r.mu.Unlock()
        return "", "", false, ErrManifestNotFound
    }
    if e.serviceKeyHash != "" && !e.serviceKeyRevoked {
        r.mu.Unlock()
        return e.serviceKeyID, "", false, nil
    }
    plaintext, keyID, err = generateServiceKey(id)
    if err != nil {
        r.mu.Unlock()
        return "", "", false, err
    }
    e.manifest.APIKey = plaintext
    e.serviceKeyID = keyID
    e.serviceKeyHash = serviceKeyHashOf(plaintext)
    e.serviceKeyPrevHash = ""
    e.serviceKeyRotatedAt = time.Now()
    e.serviceKeyRevoked = false
    r.mu.Unlock()
    if err := r.save(); err != nil {
        slog.Error("shell manifest registry save failed", "path", r.path, "error", err)
    }
    slog.Info("shell service key issued", "id", id, "key_id", keyID)
    return keyID, plaintext, true, nil
}

// RotateServiceKey replaces the service key of a dynamic registration. The
// previous key stays heartbeat-valid for grace (dual-active window) or until
// the first heartbeat arrives carrying the new key, whichever comes first.
// Plaintext returned exactly once. Static/builtin manifests are rejected —
// their credentials are config-owned.
func (r *Registry) RotateServiceKey(id string, grace time.Duration) (keyID, plaintext string, err error) {
    r.mu.Lock()
    e, ok := r.dynamic[id]
    if !ok {
        r.mu.Unlock()
        return "", "", ErrManifestNotFound
    }
    if _, isBuiltin := r.builtins[id]; isBuiltin {
        r.mu.Unlock()
        return "", "", ErrManifestStatic
    }
    if e.serviceKeyHash == "" || e.serviceKeyRevoked {
        r.mu.Unlock()
        return "", "", ErrNoServiceKey
    }
    plaintext, keyID, err = generateServiceKey(id)
    if err != nil {
        r.mu.Unlock()
        return "", "", err
    }
    e.manifest.APIKey = plaintext
    e.serviceKeyPrevHash = e.serviceKeyHash
    e.serviceKeyID = keyID
    e.serviceKeyHash = serviceKeyHashOf(plaintext)
    e.serviceKeyRotatedAt = time.Now()
    r.mu.Unlock()
    if err := r.save(); err != nil {
        slog.Error("shell manifest registry save failed", "path", r.path, "error", err)
    }
    slog.Info("shell service key rotated", "id", id, "key_id", keyID, "grace", grace.String())
    return keyID, plaintext, nil
}

// RevokeServiceKey immediately invalidates the service key: hashes and the
// plaintext credential are cleared, the entry is marked revoked (proxy serves
// the structured 503 module_key_revoked, A5 degradation) and heartbeats with
// any fm- key are refused. Static/builtin manifests are rejected.
func (r *Registry) RevokeServiceKey(id string) error {
    r.mu.Lock()
    e, ok := r.dynamic[id]
    if !ok {
        r.mu.Unlock()
        return ErrManifestNotFound
    }
    if _, isBuiltin := r.builtins[id]; isBuiltin {
        r.mu.Unlock()
        return ErrManifestStatic
    }
    if e.serviceKeyHash == "" {
        r.mu.Unlock()
        return ErrNoServiceKey
    }
    e.manifest.APIKey = ""
    e.serviceKeyID = ""
    e.serviceKeyHash = ""
    e.serviceKeyPrevHash = ""
    e.serviceKeyRotatedAt = time.Time{}
    e.serviceKeyRevoked = true
    r.mu.Unlock()
    if err := r.save(); err != nil {
        slog.Error("shell manifest registry save failed", "path", r.path, "error", err)
    }
    slog.Info("shell service key revoked", "id", id)
    return nil
}

// AuthServiceKey validates a presented fm- key for the heartbeat of module
// id: the hash must match the current key, or the pre-rotation key inside
// the dual-active grace window. A match on the CURRENT key retires the
// previous key early (the module has migrated). Revoked or never-issued
// entries never authenticate.
func (r *Registry) AuthServiceKey(id, hash string, grace time.Duration) bool {
    if hash == "" {
        return false
    }
    r.mu.Lock()
    defer r.mu.Unlock()
    e, ok := r.dynamic[id]
    if !ok || e.serviceKeyRevoked {
        return false
    }
    if hash == e.serviceKeyHash {
        if e.serviceKeyPrevHash != "" {
            e.serviceKeyPrevHash = ""
            slog.Info("shell service key previous key retired (module migrated to rotated key)", "id", id)
        }
        return true
    }
    if e.serviceKeyPrevHash != "" && hash == e.serviceKeyPrevHash {
        if time.Since(e.serviceKeyRotatedAt) <= grace {
            return true
        }
        slog.Warn("shell service key grace window expired for previous key", "id", id)
    }
    return false
}

// DynamicRegistered reports whether a dynamic registration exists for id
// (#369): the heartbeat auth path distinguishes "key invalid" (403, the
// module should fix its credential) from "registration gone" (404, the
// module should re-register) so modules get a programmable recovery signal.
func (r *Registry) DynamicRegistered(id string) bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    _, ok := r.dynamic[id]
    return ok
}

// IsServiceKeyRevoked reports whether the dynamic registration's service key
// was revoked — the proxy uses this to serve the structured 503 instead of
// forwarding an unauthenticated request at a module that can no longer
// identify its callers.
func (r *Registry) IsServiceKeyRevoked(id string) bool {
    r.mu.RLock()
    defer r.mu.RUnlock()
    e, ok := r.dynamic[id]
    return ok && e.serviceKeyRevoked
}

// ServiceKeyID exposes the non-secret key id of a dynamic registration
// ("" when none) for list surfaces.
func (r *Registry) ServiceKeyID(id string) string {
    r.mu.RLock()
    defer r.mu.RUnlock()
    if e, ok := r.dynamic[id]; ok {
        return e.serviceKeyID
    }
    return ""
}

// IsServiceKeyPlaintext reports whether plaintext looks like a gateway-issued
// module service key (fm- prefix) — the heartbeat auth path uses it to pick
// the hash-compare branch.
func IsServiceKeyPlaintext(plaintext string) bool {
    return strings.HasPrefix(plaintext, serviceKeyPrefix)
}
