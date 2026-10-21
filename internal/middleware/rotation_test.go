package middleware

import (
    "crypto/sha256"
    "encoding/hex"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/adapter"
    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/store"
)

// #262 K2: dual-active rotation grace for store-managed keys.

func hashOf(t *testing.T, raw string) string {
    t.Helper()
    sum := sha256.Sum256([]byte(raw))
    return hex.EncodeToString(sum[:])
}

func TestAPIKeyAuthWithStore_RotationGrace(t *testing.T) {
    newKey := "fk-new-rotation-key-0001"
    oldKey := "fk-old-rotation-key-0001"
    now := time.Now()

    run := func(entry *store.APIKeyEntry, present string) int {
        st := &mockKeyLookupStore{entry: entry}
        cfg := &config.AuthConfig{Enabled: true}
        handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        req.Header.Set("Authorization", "Bearer "+present)
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        return rec.Code
    }
    baseEntry := func(rotatedAt *time.Time) *store.APIKeyEntry {
        return &store.APIKeyEntry{
            Name:        "rotated-key",
            Status:      "active",
            KeyHash:     hashOf(t, newKey),
            KeyPrevHash: hashOf(t, oldKey),
            KeyRotatedAt: rotatedAt,
        }
    }

    t.Run("new_key_always_valid", func(t *testing.T) {
        if code := run(baseEntry(&now), newKey); code != http.StatusOK {
            t.Fatalf("current key: %d, want 200", code)
        }
    })
    t.Run("old_key_inside_grace", func(t *testing.T) {
        if code := run(baseEntry(&now), oldKey); code != http.StatusOK {
            t.Fatalf("pre-rotation key inside default 24h grace: %d, want 200", code)
        }
    })
    t.Run("old_key_outside_grace", func(t *testing.T) {
        stale := now.Add(-25 * time.Hour)
        if code := run(baseEntry(&stale), oldKey); code != http.StatusUnauthorized {
            t.Fatalf("pre-rotation key past grace: %d, want 401", code)
        }
    })
    t.Run("old_key_nil_rotated_at_rejected", func(t *testing.T) {
        if code := run(baseEntry(nil), oldKey); code != http.StatusUnauthorized {
            t.Fatalf("prev hash without rotation timestamp: %d, want 401 (fail-closed)", code)
        }
    })
    t.Run("configured_grace_window", func(t *testing.T) {
        st := &mockKeyLookupStore{entry: baseEntry(&now)}
        cfg := &config.AuthConfig{Enabled: true, KeyRotationGrace: time.Minute}
        handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
        req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
        req.Header.Set("Authorization", "Bearer "+oldKey)
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        if rec.Code != http.StatusOK {
            t.Fatalf("pre-rotation key inside configured 1m grace: %d, want 200", rec.Code)
        }
    })
}

func TestAPIKeyAuthWithStore_RevokedKeyRejected(t *testing.T) {
    raw := "fk-revoked-key-0001"
    st := &mockKeyLookupStore{entry: &store.APIKeyEntry{
        Name:     "revoked-key",
        Status:   "revoked",
        KeyHash:  hashOf(t, raw),
    }}
    cfg := &config.AuthConfig{Enabled: true}
    handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req.Header.Set("Authorization", "Bearer "+raw)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("revoked key: %d, want 401", rec.Code)
    }
}

// TestAPIKeyAuthWithStore_PrincipalPropagated (#262 K2): after store-key
// auth, the derived ctx carries the key name via adapter.WithPrincipal, so
// InjectFusionHeaders stamps X-Fusion-Principal on the outbound request.
func TestAPIKeyAuthWithStore_PrincipalPropagated(t *testing.T) {
    raw := "fk-principal-key-0001"
    st := &mockKeyLookupStore{entry: &store.APIKeyEntry{
        Name:    "principal-key",
        Status:  "active",
        KeyHash: hashOf(t, raw),
        Scopes:  []string{"chat"},
    }}
    cfg := &config.AuthConfig{Enabled: true}
    var outbound http.Request
    handler := APIKeyAuthWithStore(cfg, st)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        outbound = http.Request{Header: http.Header{}}
        adapter.InjectFusionHeaders(r.Context(), &outbound)
    }))
    req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
    req.Header.Set("Authorization", "Bearer "+raw)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("auth: %d", rec.Code)
    }
    if got := outbound.Header.Get("X-Fusion-Principal"); got != "principal-key" {
        t.Fatalf("X-Fusion-Principal: got %q, want principal-key", got)
    }
}
