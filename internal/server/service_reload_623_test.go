package server

// #623: /admin/config/reload reported success while service_routes additions
// were silently ignored — the route table mounts on the mux once at boot
// (ServeMux cannot unregister), so reload-only changes never served (404 via
// the SPA catch-all). The reload response must enumerate the boot-only
// sections a diff touched ("restart_required": ["service_routes"]) and log a
// warning, so the operator is told to restart instead of being acked into a
// green 404.

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "slices"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

const cfg623Base = `
server:
  host: "0.0.0.0"
  port: 11432
  log_level: "info"
  graceful_shutdown_timeout: 15
  max_request_body_size: 5242880
routing:
  token_threshold: 8000
  output_input_ratio_threshold: 0.6
  local_priority:
    enabled: true
    max_system_memory_ratio: 0.9
    max_mlx_memory_ratio: 0.7
    max_concurrent: 8
    swap_page_rate_threshold: 100
`

func reload623(t *testing.T, s *Server, yamlExtra string) map[string]any {
    t.Helper()
    cfgFile := filepath.Join(t.TempDir(), "config.yaml")
    if err := os.WriteFile(cfgFile, []byte(cfg623Base+yamlExtra), 0644); err != nil {
        t.Fatal(err)
    }
    s.cfgPath = cfgFile
    req := httptest.NewRequest(http.MethodPost, "/admin/config/reload", nil)
    rec := httptest.NewRecorder()
    s.handleConfigReload(rec, req)
    if rec.Code != http.StatusOK {
        t.Fatalf("reload = %d body=%s", rec.Code, rec.Body.String())
    }
    var resp map[string]any
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatalf("invalid json: %v body=%s", err, rec.Body.String())
    }
    return resp
}

func Test623_ReloadServiceRoutesChanged_RestartRequired(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.ServiceRoutes = map[string]config.ServiceRouteConfig{
        "existing": {URL: "http://127.0.0.1:11461", UpstreamAuth: "user"},
    }
    resp := reload623(t, s, `
service_routes:
  ad4probe:
    url: "http://127.0.0.1:11461"
    upstream_auth: user
`)
    rr, ok := resp["restart_required"].([]any)
    if !ok {
        t.Fatalf("restart_required missing from reload response: %v", resp)
    }
    var sections []string
    for _, v := range rr {
        sections = append(sections, v.(string))
    }
    if !slices.Contains(sections, "service_routes") {
        t.Fatalf("restart_required = %v, want service_routes listed", sections)
    }
}

func Test623_ReloadServiceRoutesUnchanged_NoFlag(t *testing.T) {
    s := newTestServer()
    s.cfg.Config.ServiceRoutes = map[string]config.ServiceRouteConfig{
        "ad4probe": {URL: "http://127.0.0.1:11461", UpstreamAuth: "user"},
    }
    resp := reload623(t, s, `
service_routes:
  ad4probe:
    url: "http://127.0.0.1:11461"
    upstream_auth: user
`)
    if _, present := resp["restart_required"]; present {
        t.Fatalf("identical service_routes must not set restart_required: %v", resp)
    }
}

// Faithful prod repro: main.go registers an OnReload handler that swaps
// s.cfg inside Reload — the handler here mimics that wiring. Before the
// capture-order fix, handleConfigReload read s.cfg AFTER Reload and compared
// the new snapshot against itself (flag never fired, caught live on
// v0.9.164). The registered handler stays for the rest of the package test
// run but only mutates THIS server instance.
func Test623_ReloadWithCfgSwapHandler_FlagFires(t *testing.T) {
    s := newTestServer()
    config.OnReload(func(old, new *config.ConfigSnapshot) {
        s.cfgMu.Lock()
        s.cfg = new
        s.cfgMu.Unlock()
    })
    resp := reload623(t, s, `
service_routes:
  ad4probe:
    url: "http://127.0.0.1:11461"
    upstream_auth: user
`)
    if _, present := resp["restart_required"]; !present {
        t.Fatalf("restart_required missing when s.cfg is swapped inside Reload (prod wiring): %v", resp)
    }
}

func Test623_ServiceRoutesChanged_Table(t *testing.T) {
    base := map[string]config.ServiceRouteConfig{
        "a": {URL: "http://u1", UpstreamAuth: "user"},
    }
    cases := []struct {
        name string
        old  map[string]config.ServiceRouteConfig
        new  map[string]config.ServiceRouteConfig
        want bool
    }{
        {"nil-nil", nil, nil, false},
        {"added", base, map[string]config.ServiceRouteConfig{"a": {URL: "http://u1", UpstreamAuth: "user"}, "b": {URL: "http://u2"}}, true},
        {"removed", base, nil, true},
        {"url-changed", base, map[string]config.ServiceRouteConfig{"a": {URL: "http://u9", UpstreamAuth: "user"}}, true},
        {"prefix-changed", base, map[string]config.ServiceRouteConfig{"a": {URL: "http://u1", UpstreamAuth: "user", Prefix: "/v1/x"}}, true},
        {"auth-changed", base, map[string]config.ServiceRouteConfig{"a": {URL: "http://u1", UpstreamAuth: "inject"}}, true},
        {"tokenfile-changed", base, map[string]config.ServiceRouteConfig{"a": {URL: "http://u1", UpstreamAuth: "inject", UpstreamTokenFile: "/tmp/s"}}, true},
        {"identical", base, map[string]config.ServiceRouteConfig{"a": {URL: "http://u1", UpstreamAuth: "user"}}, false},
    }
    for _, tc := range cases {
        if got := serviceRoutesChanged(tc.old, tc.new); got != tc.want {
            t.Errorf("%s: serviceRoutesChanged = %v, want %v", tc.name, got, tc.want)
        }
    }
}
