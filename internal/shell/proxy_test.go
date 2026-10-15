package shell

import (
    "context"
    "net/http"
    "net/http/httptest"
    "strings"
    "sync/atomic"
    "testing"
    "time"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func TestProxy_ForwardsToUpstream(t *testing.T) {
    var gotPath string
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotPath = r.URL.Path
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("hello-from-module"))
    }))
    defer upstream.Close()

    p := NewProxy(func(string) (string, string) { return upstream.URL, "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/index.html", nil))
    if w.Code != http.StatusOK || w.Body.String() != "hello-from-module" {
        t.Fatalf("code=%d body=%q", w.Code, w.Body.String())
    }
    if gotPath != "/index.html" {
        t.Fatalf("upstream path = %q", gotPath)
    }
}

func TestProxy_UpstreamDownEnvelope(t *testing.T) {
    p := NewProxy(func(id string) (string, string) {
        if id == "fusion-dead" {
            return "http://127.0.0.1:1", ""
        }
        return "", ""
    })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-dead/api/models", nil))
    if w.Code != http.StatusServiceUnavailable {
        t.Fatalf("code = %d, want 503", w.Code)
    }
    body := w.Body.String()
    for _, want := range []string{`"code":"backend_unavailable"`, `"module_id":"fusion-dead"`, `"retryable":true`} {
        if !strings.Contains(body, want) {
            t.Errorf("envelope missing %s: %s", want, body)
        }
    }
}

func TestProxy_ModuleNotMapped(t *testing.T) {
    p := NewProxy(func(string) (string, string) { return "", "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-unknown/x", nil))
    if w.Code != http.StatusNotFound {
        t.Fatalf("code = %d, want 404", w.Code)
    }
    if !strings.Contains(w.Body.String(), `"code":"module_not_found"`) {
        t.Fatalf("body = %s", w.Body.String())
    }
}

func TestProxy_MissingModuleID(t *testing.T) {
    p := NewProxy(func(string) (string, string) { return "http://127.0.0.1:1", "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/", nil))
    if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "missing module id") {
        t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
    }
}

func TestProxy_InvalidUpstreamURL(t *testing.T) {
    p := NewProxy(func(string) (string, string) { return "not-a-url", "" })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-x/y", nil))
    if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "upstream_invalid") {
        t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
    }
}

func TestHealthPoller_RecordsObservations(t *testing.T) {
    var hits atomic.Int64
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        hits.Add(1)
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()

    m := validManifest("fusion-mlx")
    m.Health = upstream.URL + "/health"
    r := NewRegistry([]config.ShellManifest{m}, nil, 30*time.Second)
    poller := NewHealthPoller(r, 10*time.Millisecond)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go poller.Run(ctx)
    time.Sleep(60 * time.Millisecond)
    if hits.Load() == 0 {
        t.Fatal("poller never probed the health URL")
    }
    views := r.List()
    if len(views) != 1 || views[0].Status != StatusOnline {
        t.Fatalf("want online dynamic registration, got %+v", views)
    }
}

func TestHealthPoller_MarksDown(t *testing.T) {
    m := validManifest("fusion-dead")
    m.Health = "http://127.0.0.1:1/health"
    r := NewRegistry([]config.ShellManifest{m}, nil, 30*time.Second)
    poller := NewHealthPoller(r, 10*time.Millisecond)
    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()
    go poller.Run(ctx)
    time.Sleep(60 * time.Millisecond)
    views := r.List()
    if views[0].Status != StatusOffline {
        t.Fatalf("want offline after failed probes, got %s", views[0].Status)
    }
}

func TestProxy_UpstreamCredentialInjection(t *testing.T) {
    var gotAuth, gotCookie string
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotAuth = r.Header.Get("Authorization")
        gotCookie = r.Header.Get("Cookie")
        _, _ = w.Write([]byte(`{"ok":true}`))
    }))
    defer upstream.Close()
    p := NewProxy(func(id string) (string, string) {
        if id == "fusion-mlx" {
            return upstream.URL, "module-secret-key"
        }
        return "", ""
    })
    req := httptest.NewRequest(http.MethodGet, "/shell/fusion-mlx/v1/models", nil)
    req.Header.Set("Authorization", "Bearer user-session-token") // caller token must NOT leak upstream
    req.Header.Set("Cookie", "fusion_shell_session=leak-me")
    w := httptest.NewRecorder()
    p.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("code = %d body=%s", w.Code, w.Body.String())
    }
    if gotAuth != "Bearer module-secret-key" {
        t.Fatalf("upstream Authorization = %q, want injected module key", gotAuth)
    }
    if gotCookie != "" {
        t.Fatalf("session cookie leaked upstream: %q", gotCookie)
    }
}
