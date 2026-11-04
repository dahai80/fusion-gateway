package server

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
    "github.com/fusion-gateway/fusion-gateway/internal/shell"
)

// catalogManifest is the minimal valid dynamic registration (schema rules
// in shell.validateManifest); fresh upsert reads online/reachable.
func catalogManifest(id string) config.ShellManifest {
    return config.ShellManifest{
        ID:       id,
        Title:    "Test " + id,
        Path:     "/" + id,
        Entry:    "http://127.0.0.1:11432/shell/" + id + "/",
        Devices:  []string{"desktop"},
        Category: "platform",
    }
}

// catalogTestServer wires the #488 merge view: supervisor pointed at a
// mock, registry optionally populated.
func catalogTestServer(t *testing.T, supURL string, manifests ...config.ShellManifest) *Server {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Catalog.SupervisorStatusURL = supURL
    cfg.Config.Catalog.StatusTTL = time.Second
    s := &Server{cfg: cfg}
    if len(manifests) > 0 {
        s.shellRegistry = shell.NewRegistry(nil, nil, 30*time.Second)
        for _, m := range manifests {
            if err := s.shellRegistry.Upsert(m); err != nil {
                t.Fatalf("upsert manifest: %v", err)
            }
        }
    }
    return s
}

func getCatalog(s *Server, target string) (int, map[string]any) {
    r := httptest.NewRequest(http.MethodGet, target, nil)
    w := httptest.NewRecorder()
    s.handleCatalog(w, r)
    var out map[string]any
    _ = json.Unmarshal(w.Body.Bytes(), &out)
    return w.Code, out
}

func productState(out map[string]any, id string) (string, bool) {
    prods, _ := out["products"].([]any)
    for _, raw := range prods {
        p, _ := raw.(map[string]any)
        if p["id"] == id {
            st, _ := p["state"].(string)
            return st, true
        }
    }
    return "", false
}

// #488 AC: the merged three-source view; supervisor Healthy/Stopped/absent
// map to running / installed_stopped / not_installed.
func TestCatalog_MergeThreeSources(t *testing.T) {
    sup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true,"services":[
            {"name":"fusion-mlx","state":"Healthy"},
            {"name":"fusion-rag","state":"Stopped"}]}`))
    }))
    defer sup.Close()
    s := catalogTestServer(t, sup.URL)
    code, out := getCatalog(s, "/v1/catalog")
    if code != http.StatusOK {
        t.Fatalf("code = %d body=%v", code, out)
    }
    if st, _ := productState(out, "fusion-mlx"); st != "running" {
        t.Fatalf("healthy supervisor state must map to running, got %q", st)
    }
    if st, _ := productState(out, "fusion-rag"); st != "installed_stopped" {
        t.Fatalf("stopped supervisor state must map to installed_stopped, got %q", st)
    }
    if st, ok := productState(out, "fusion-cowork"); !ok || st != "not_installed" {
        t.Fatalf("absent product must be not_installed, got %q ok=%v", st, ok)
    }
    src, _ := out["sources"].(map[string]any)
    if src["catalog"] != true || src["supervisor"] != true {
        t.Fatalf("healthy sources must be flagged, got %v", src)
    }
}

// #488 AC: registered-but-not-cataloged module appears as a dynamic row;
// a manifest-registered reachable module reads running even when the
// supervisor is absent.
func TestCatalog_DynamicRowAndRegistryFallback(t *testing.T) {
    s := catalogTestServer(t, "http://127.0.0.1:1/unreachable", catalogManifest("fusion-custom"))
    code, out := getCatalog(s, "/v1/catalog")
    if code != http.StatusOK {
        t.Fatalf("code = %d", code)
    }
    if st, ok := productState(out, "fusion-custom"); !ok || st != "running" {
        t.Fatalf("registered reachable module must read running, got %q ok=%v", st, ok)
    }
    src, _ := out["sources"].(map[string]any)
    if src["supervisor"] != false || src["manifests"] != true {
        t.Fatalf("degraded supervisor must be flagged, got %v", src)
    }
}

// #488 AC: graceful degradation — supervisor absent + zero modules still
// answers 200 with the catalog and per-source degraded flags, never 5xx.
func TestCatalog_DegradeNeverFails(t *testing.T) {
    s := catalogTestServer(t, "http://127.0.0.1:1/unreachable")
    code, out := getCatalog(s, "/v1/catalog")
    if code != http.StatusOK {
        t.Fatalf("degraded sources must never 5xx, got %d", code)
    }
    prods, _ := out["products"].([]any)
    if len(prods) < 10 {
        t.Fatalf("catalog rows must still serve, got %d", len(prods))
    }
    if st, ok := productState(out, "fusion-mlx"); !ok || st != "" {
        t.Fatalf("degraded supervisor must leave state unknown, got %q ok=%v", st, ok)
    }
    src, _ := out["sources"].(map[string]any)
    if src["supervisor"] != false {
        t.Fatalf("unreachable supervisor must be flagged, got %v", src)
    }
    if reason, _ := src["supervisor_reason"].(string); reason == "" {
        t.Fatalf("degraded source must carry a reason, got %v", src)
    }
}

// #488: the supervisor fetch is cached for StatusTTL — two calls hit the
// supervisor once.
func TestCatalog_SupervisorCacheTTL(t *testing.T) {
    hits := 0
    sup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        hits++
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte(`{"ok":true,"services":[]}`))
    }))
    defer sup.Close()
    s := catalogTestServer(t, sup.URL)
    s.cfg.Config.Catalog.StatusTTL = time.Minute
    _, _ = getCatalog(s, "/v1/catalog")
    _, _ = getCatalog(s, "/v1/catalog")
    if hits != 1 {
        t.Fatalf("supervisor fetch must be cached within TTL, got %d hits", hits)
    }
}
