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
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// #284 handler-level acceptance: a heartbeat carrying the module's current
// manifest converges the stored ext fields (pages drift), the empty-body
// keepalive keeps working, an id-less JSON body is treated as a plain ping,
// and a rejected refresh still counts for liveness (400 + online).
func TestShellHeartbeat_ManifestRefresh(t *testing.T) {
    s := newShellTestServer(t, false)
    s.cfg.Config.Auth.MasterKey = "mk"
    admin := &middleware.Principal{IsMaster: true, KeyConfig: &config.AuthKeyConfig{Key: "mk", Name: "master"}}

    manifest := map[string]any{
        "id": "fusion-cowork", "title": "Cowork", "path": "/cowork",
        "entry": "http://127.0.0.1:11432/shell/fusion-cowork/",
        "upstream": "http://127.0.0.1:19000",
        "devices": []string{"desktop"}, "category": "platform",
        "pages": []map[string]any{{"title": "P1", "route": "/cowork/p1"}},
    }
    body, _ := json.Marshal(manifest)
    req := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests", bytes.NewReader(body))
    req = req.WithContext(middleware.ContextWithPrincipal(req.Context(), admin))
    w := httptest.NewRecorder()
    s.withShellManifestAuth(s.handleShellManifestUpsert)(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("registration code = %d body=%s", w.Code, w.Body.String())
    }

    beat := func(payload string) *httptest.ResponseRecorder {
        var rdr *bytes.Reader
        if payload == "" {
            rdr = bytes.NewReader(nil)
        } else {
            rdr = bytes.NewReader([]byte(payload))
        }
        r := httptest.NewRequest(http.MethodPost, "/api/v1/shell/manifests/fusion-cowork/heartbeat", rdr)
        r.SetPathValue("id", "fusion-cowork")
        r.Header.Set("Authorization", "Bearer mk")
        ww := httptest.NewRecorder()
        s.withShellHeartbeatAuth(s.handleShellManifestHeartbeat)(ww, r)
        return ww
    }

    pages := func() int {
        for _, v := range s.shellRegistry.List() {
            if v.ID == "fusion-cowork" {
                return len(v.Pages)
            }
        }
        return -1
    }

    // Empty body: plain keepalive, no field changes.
    if ww := beat(""); ww.Code != http.StatusOK {
        t.Fatalf("empty-body heartbeat code = %d body=%s", ww.Code, ww.Body.String())
    }
    if got := pages(); got != 1 {
        t.Fatalf("empty body must not change pages, got %d", got)
    }

    // Id-less JSON body: treated as plain ping (back-compat).
    if ww := beat(`{}`); ww.Code != http.StatusOK {
        t.Fatalf("id-less JSON heartbeat code = %d body=%s", ww.Code, ww.Body.String())
    }

    // Heartbeat carrying the grown manifest: fields converge.
    manifest["pages"] = []map[string]any{
        {"title": "P1", "route": "/cowork/p1"},
        {"title": "Skills", "route": "/cowork/skills"},
        {"title": "Extensions", "route": "/cowork/extensions"},
    }
    grown, _ := json.Marshal(manifest)
    ww := beat(string(grown))
    if ww.Code != http.StatusOK {
        t.Fatalf("manifest heartbeat code = %d body=%s", ww.Code, ww.Body.String())
    }
    if !strings.Contains(ww.Body.String(), "manifest refreshed") {
        t.Fatalf("response must state the refresh, body=%s", ww.Body.String())
    }
    if got := pages(); got != 3 {
        t.Fatalf("pages must converge to 3, got %d", got)
    }

    // Malformed JSON body: 400, loud.
    if ww := beat(`{"id":`); ww.Code != http.StatusBadRequest {
        t.Fatalf("malformed body code = %d, want 400", ww.Code)
    }

    // Invalid manifest (bad category): 400 with the validation error, but
    // liveness still recorded — the module stays online and observable.
    manifest["category"] = "bogus"
    bad, _ := json.Marshal(manifest)
    wbad := beat(string(bad))
    if wbad.Code != http.StatusBadRequest {
        t.Fatalf("invalid manifest code = %d, want 400 body=%s", wbad.Code, wbad.Body.String())
    }
    var env shell.Envelope
    _ = json.Unmarshal(wbad.Body.Bytes(), &env)
    if env.Code != "manifest_refresh_rejected" {
        t.Fatalf("envelope code = %q, want manifest_refresh_rejected", env.Code)
    }
    for _, v := range s.shellRegistry.List() {
        if v.ID == "fusion-cowork" && v.Unreachable {
            t.Fatal("rejected refresh must still count for liveness (module must stay online)")
        }
    }
}
