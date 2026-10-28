package middleware

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/observability"
)

// TestAPIKeyAuthWithStore_InvalidKeySurfacesHashPrefix (#282): a stale/unknown
// key must 401 with the key's hash prefix in the response body (so client-side
// tooling can self-diagnose a rotated key without log access) and increment
// fusion_gateway_auth_invalid_key_total{hash_prefix} so a recurring stale key
// is spottable in /metrics. The prefix is 8 hex chars of the SHA-256 — it
// identifies the key family, never the key.
func TestAPIKeyAuthWithStore_InvalidKeySurfacesHashPrefix(t *testing.T) {
    staleKey := "fg-stale-key-from-old-settings"
    sum := sha256.Sum256([]byte(staleKey))
    wantPrefix := hex.EncodeToString(sum[:])[:8]

    cfg := &config.AuthConfig{Enabled: true}
    handler := APIKeyAuthWithStore(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("invalid key must not reach downstream handler")
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    req.Header.Set("Authorization", "Bearer "+staleKey)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)

    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("status = %d, want 401", rec.Code)
    }
    var body struct {
        Error struct {
            Message      string `json:"message"`
            Type         string `json:"type"`
            KeyHashPrefix string `json:"key_hash_prefix"`
        } `json:"error"`
    }
    if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
        t.Fatalf("401 body decode: %v body=%s", err, rec.Body.String())
    }
    if body.Error.KeyHashPrefix != wantPrefix {
        t.Fatalf("key_hash_prefix = %q, want %q", body.Error.KeyHashPrefix, wantPrefix)
    }
    if body.Error.Type != "auth_error" {
        t.Fatalf("type = %q, want auth_error", body.Error.Type)
    }

    req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec2 := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec2, req2)
    want := `fusion_gateway_auth_invalid_key_total{hash_prefix="` + wantPrefix + `"}`
    if !strings.Contains(rec2.Body.String(), want) {
        t.Fatalf("metric sample %q missing from /metrics", want)
    }
}

// TestAPIKeyAuthWithStore_MissingKeyNoPrefix: a request with no key at all
// has nothing to hash — the 401 must stay the plain missing-key error with
// no key_hash_prefix field.
func TestAPIKeyAuthWithStore_MissingKeyNoPrefix(t *testing.T) {
    cfg := &config.AuthConfig{Enabled: true}
    handler := APIKeyAuthWithStore(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("missing key must not reach downstream handler")
    }))
    req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
    rec := httptest.NewRecorder()
    handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusUnauthorized {
        t.Fatalf("status = %d, want 401", rec.Code)
    }
    if strings.Contains(rec.Body.String(), "key_hash_prefix") {
        t.Fatalf("missing-key 401 must not carry key_hash_prefix, body=%s", rec.Body.String())
    }
}

// TestAPIKeyAuthWithStore_InvalidKeyThrottleKeeps401 (#377): repeated
// invalid-key polls get their WARN log line throttled per hash_prefix (5 min),
// but the fail-closed 401 response — with the self-diagnosis hash prefix —
// must fire identically on every request, and the metric must keep counting.
func TestAPIKeyAuthWithStore_InvalidKeyThrottleKeeps401(t *testing.T) {
    staleKey := "fg-stale-key-throttle-probe-377"
    sum := sha256.Sum256([]byte(staleKey))
    wantPrefix := hex.EncodeToString(sum[:])[:8]

    cfg := &config.AuthConfig{Enabled: true}
    handler := APIKeyAuthWithStore(cfg, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        t.Fatal("invalid key must not reach downstream handler")
    }))
    for i := 0; i < 3; i++ {
        req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
        req.Header.Set("Authorization", "Bearer "+staleKey)
        rec := httptest.NewRecorder()
        handler.ServeHTTP(rec, req)
        if rec.Code != http.StatusUnauthorized {
            t.Fatalf("poll %d: status = %d, want 401", i, rec.Code)
        }
        if !strings.Contains(rec.Body.String(), `"key_hash_prefix":"`+wantPrefix+`"`) {
            t.Fatalf("poll %d: 401 body missing key_hash_prefix, body=%s", i, rec.Body.String())
        }
    }
    // The metric counts every occurrence even while the log line is throttled.
    req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec2 := httptest.NewRecorder()
    observability.Handler().ServeHTTP(rec2, req2)
    want := `fusion_gateway_auth_invalid_key_total{hash_prefix="` + wantPrefix + `"} 3`
    if !strings.Contains(rec2.Body.String(), want) {
        t.Fatalf("metric must count all 3 rejections, want sample %q", want)
    }
}
