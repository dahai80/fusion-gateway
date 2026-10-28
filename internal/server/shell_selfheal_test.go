package server

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// #369: heartbeat denials must be programmable recovery signals — 404
// manifest_unknown (re-register), 403 heartbeat_key_revoked (admin rotate),
// 403 heartbeat_forbidden (fix credential) — and a module holding a valid
// fm- service key may re-register its own id without any admin credential.

func selfHealManifest(id string) map[string]any {
    return map[string]any{
        "id": id, "title": "Demo", "path": "/demo",
        "entry": "http://127.0.0.1:11432/shell/" + id + "/", "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
    }
}

// selfHealRegister registers a manifest with the admin principal and returns
// the issued raw fm- key.
func selfHealRegister(t *testing.T, s *Server, id string) string {
    t.Helper()
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}
    body, _ := json.Marshal(selfHealManifest(id))
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestUpsertAuth(s.handleShellManifestUpsert)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("registration code = %d body=%s", w.Code, w.Body.String())
    }
    var kr shellKeyResponse
    _ = json.Unmarshal(w.Body.Bytes(), &kr)
    if kr.RawKey == "" {
        t.Fatal("expected issued fm- key")
    }
    return kr.RawKey
}

func selfHealBeat(t *testing.T, s *Server, id, token string) *httptest.ResponseRecorder {
    t.Helper()
    r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/"+id+"/heartbeat", nil)
    r.SetPathValue("id", id)
    if token != "" {
        r.Header.Set("Authorization", "Bearer "+token)
    }
    ww := httptest.NewRecorder()
    s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
    return ww
}

// TestShellHeartbeat_UnknownManifestReturns404: an fm- key for an id with no
// registration gets 404 manifest_unknown (not the ambiguous 403) so the
// module knows to re-register.
func TestShellHeartbeat_UnknownManifestReturns404(t *testing.T) {
    s := newShellTestServer(t, false)
    w := selfHealBeat(t, s, "fusion-ghost", "fm-fusion-ghost-00000000000000000000000000000000")
    if w.Code != http.StatusNotFound {
        t.Fatalf("code = %d, want 404", w.Code)
    }
    if !strings.Contains(w.Body.String(), "manifest_unknown") {
        t.Fatalf("expected manifest_unknown code, body: %s", w.Body.String())
    }
}

// TestShellHeartbeat_RevokedKeyDistinct403: a revoked key is 403
// heartbeat_key_revoked, distinct from a plain key mismatch.
func TestShellHeartbeat_RevokedKeyDistinct403(t *testing.T) {
    s := newShellTestServer(t, false)
    raw := selfHealRegister(t, s, "fusion-demo")
    if err := s.shellRegistry.RevokeServiceKey("fusion-demo"); err != nil {
        t.Fatal(err)
    }
    w := selfHealBeat(t, s, "fusion-demo", raw)
    if w.Code != http.StatusForbidden {
        t.Fatalf("code = %d, want 403", w.Code)
    }
    if !strings.Contains(w.Body.String(), "heartbeat_key_revoked") {
        t.Fatalf("expected heartbeat_key_revoked, body: %s", w.Body.String())
    }
}

// TestShellHeartbeat_WrongKeyStill403: a mismatched key for a live
// registration stays 403 heartbeat_forbidden.
func TestShellHeartbeat_WrongKeyStill403(t *testing.T) {
    s := newShellTestServer(t, false)
    selfHealRegister(t, s, "fusion-demo")
    w := selfHealBeat(t, s, "fusion-demo", "fm-fusion-demo-00000000000000000000000000000000")
    if w.Code != http.StatusForbidden {
        t.Fatalf("code = %d, want 403", w.Code)
    }
    if !strings.Contains(w.Body.String(), "heartbeat_forbidden") {
        t.Fatalf("expected heartbeat_forbidden, body: %s", w.Body.String())
    }
}

// TestShellUpsert_ServiceKeySelfHeal: a module holding its valid fm- key may
// re-register its own id without any admin credential; the same key may not
// register a DIFFERENT id.
func TestShellUpsert_ServiceKeySelfHeal(t *testing.T) {
    s := newShellTestServer(t, false)
    raw := selfHealRegister(t, s, "fusion-demo")

    // No principal at all — only the fm- Bearer authenticates the upsert.
    body, _ := json.Marshal(selfHealManifest("fusion-demo"))
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+raw)
    w := httptest.NewRecorder()
    s.withShellManifestUpsertAuth(s.handleShellManifestUpsert)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("self-heal re-register code = %d body=%s, want 200", w.Code, w.Body.String())
    }

    // Same key, different id: denied.
    body2, _ := json.Marshal(selfHealManifest("fusion-other"))
    req2 := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body2))
    req2.Header.Set("Authorization", "Bearer "+raw)
    w2 := httptest.NewRecorder()
    s.withShellManifestUpsertAuth(s.handleShellManifestUpsert)(w2, req2)
    if w2.Code != http.StatusForbidden {
        t.Fatalf("cross-id re-register code = %d, want 403", w2.Code)
    }
}
