package server

import (
    "bytes"
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/middleware"
)

// #484: after a service-key rotation, the module holding the pre-rotation
// key keeps getting 200 heartbeats inside the grace window while every
// proxied call fails with Unauthorized (the proxy injects the NEW key).
// The heartbeat 200 must say so — X-Fusion-Key-State: stale — so the module
// can re-register proactively instead of trusting a lying liveness signal.

func TestShellHeartbeat_KeyStateStale(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Auth.MasterKey = "mk"
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}

    manifest := map[string]any{
        "id": "fusion-cowork", "title": "Cowork", "path": "/cowork",
        "entry": "http://127.0.0.1:11432/shell/fusion-cowork/",
        "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(manifest)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("registration code = %d body=%s", w.Code, w.Body.String())
    }
    var reg struct {
        RawKey string `json:"raw_key"`
    }
    if err := json.Unmarshal(w.Body.Bytes(), &reg); err != nil || reg.RawKey == "" {
        t.Fatalf("registration must return the issued raw key: err=%v body=%s", err, w.Body.String())
    }

    rotate := func() string {
        rr := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-cowork/key/rotate", nil)
        rr.SetPathValue("id", "fusion-cowork")
        rr = rr.WithContext(middleware.ContextWithPrincipal(rr.Context(), admin))
        ww := httptest.NewRecorder()
        s.handleShellServiceKeyRotate(ww, rr)
        if ww.Code != http.StatusOK {
            t.Fatalf("rotate code = %d body=%s", ww.Code, ww.Body.String())
        }
        var out struct {
            RawKey string `json:"raw_key"`
        }
        if err := json.Unmarshal(ww.Body.Bytes(), &out); err != nil || out.RawKey == "" {
            t.Fatalf("rotate must return the new raw key: err=%v body=%s", err, ww.Body.String())
        }
        return out.RawKey
    }
    beat := func(key string) *httptest.ResponseRecorder {
        r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-cowork/heartbeat", nil)
        r.SetPathValue("id", "fusion-cowork")
        r.Header.Set("Authorization", "Bearer "+key)
        ww := httptest.NewRecorder()
        s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
        return ww
    }

    newKey := rotate()

    // Old (rotated-out) key inside the grace window: still 200, but the
    // response must flag the staleness.
    ww := beat(reg.RawKey)
    if ww.Code != http.StatusOK {
        t.Fatalf("stale-key heartbeat code = %d body=%s", ww.Code, ww.Body.String())
    }
    if got := ww.Header().Get("X-Fusion-Key-State"); got != "stale" {
        t.Fatalf("stale-key heartbeat X-Fusion-Key-State = %q, want stale (body=%s)", got, ww.Body.String())
    }
    if !bytes.Contains(ww.Body.Bytes(), []byte("STALE")) {
        t.Fatalf("stale-key heartbeat body must spell out the staleness: %s", ww.Body.String())
    }

    // Migrating to the new key clears the flag.
    ww = beat(newKey)
    if ww.Code != http.StatusOK {
        t.Fatalf("new-key heartbeat code = %d body=%s", ww.Code, ww.Body.String())
    }
    if got := ww.Header().Get("X-Fusion-Key-State"); got != "current" {
        t.Fatalf("new-key heartbeat X-Fusion-Key-State = %q, want current", got)
    }
    // And the old key is retired: no more stale signal, straight 403.
    ww = beat(reg.RawKey)
    if ww.Code != http.StatusForbidden {
        t.Fatalf("retired-key heartbeat code = %d, want 403 (body=%s)", ww.Code, ww.Body.String())
    }
}

func TestShellHeartbeat_KeyStateHeaderAbsentForAdmin(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Auth.MasterKey = "mk"
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}

    manifest := map[string]any{
        "id": "fusion-cowork", "title": "Cowork", "path": "/cowork",
        "entry": "http://127.0.0.1:11432/shell/fusion-cowork/",
        "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
    }
    body, _ := json.Marshal(manifest)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("registration code = %d body=%s", w.Code, w.Body.String())
    }

    r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-cowork/heartbeat", nil)
    r.SetPathValue("id", "fusion-cowork")
    r.Header.Set("Authorization", "Bearer mk")
    ww := httptest.NewRecorder()
    s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
    if ww.Code != http.StatusOK {
        t.Fatalf("admin heartbeat code = %d body=%s", ww.Code, ww.Body.String())
    }
    if got := ww.Header().Get("X-Fusion-Key-State"); got != "" {
        t.Fatalf("admin-token heartbeat must not carry X-Fusion-Key-State, got %q", got)
    }
}
