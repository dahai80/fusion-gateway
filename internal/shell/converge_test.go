package shell

// #669 regression: when an upstream 401s the injected dynamic credential,
// the proxy fires the convergence hook and the registry adopts the static
// api_key — divergence self-heals instead of wedging every embedded console
// call until a manual registration-row delete.

import (
    "net/http"
    "net/http/httptest"
    "sync/atomic"
    "testing"
    "time"
)

func Test669_ConvergeCredential_SwapsFailedDynamicKey(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, time.Second)
    m := validManifest("fusion-security")
    m.Upstream = "http://127.0.0.1:11454"
    m.APIKey = "stale-issued-key"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert failed: %v", err)
    }
    if !r.ConvergeCredential("fusion-security", "stale-issued-key", "static-key") {
        t.Fatal("converge should succeed when failedKey is authoritative")
    }
    key, ok := r.CredentialFor("fusion-security")
    if !ok || key != "static-key" {
        t.Fatalf("credential = %q ok=%v, want static-key", key, ok)
    }
    // Re-converging with the same static key is a no-op (already adopted).
    if r.ConvergeCredential("fusion-security", "stale-issued-key", "static-key") {
        t.Fatal("failedKey no longer authoritative — second converge must refuse")
    }
}

func Test669_ConvergeCredential_GuardsReject(t *testing.T) {
    r := NewRegistry(nil, []string{"http://127.0.0.1:11432"}, time.Second)
    m := validManifest("fusion-security")
    m.Upstream = "http://127.0.0.1:11454"
    m.APIKey = "issued-key"
    if err := r.Upsert(m); err != nil {
        t.Fatalf("upsert failed: %v", err)
    }
    if r.ConvergeCredential("fusion-unknown", "issued-key", "static-key") {
        t.Fatal("no dynamic entry — must refuse")
    }
    if r.ConvergeCredential("fusion-security", "wrong-failed-key", "static-key") {
        t.Fatal("failedKey not authoritative — must refuse")
    }
    if r.ConvergeCredential("fusion-security", "issued-key", "") {
        t.Fatal("empty newKey — must refuse")
    }
    if r.ConvergeCredential("fusion-security", "issued-key", "issued-key") {
        t.Fatal("newKey == failedKey — must refuse")
    }
    if key, _ := r.CredentialFor("fusion-security"); key != "issued-key" {
        t.Fatalf("credential mutated by refused converge: %q", key)
    }
}

func Test669_Proxy401FiresConvergence(t *testing.T) {
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusUnauthorized)
    }))
    defer upstream.Close()

    var converged atomic.Value // string
    converged.Store("")
    p := NewProxy(func(string) (string, string) { return upstream.URL, "stale-dynamic-key" })
    p.SetCredentialConverger(func(moduleID, failedKey string) {
        converged.Store(failedKey)
    })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-security/api/v1/admin/tenants", nil))
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("401 must pass through, got %d", w.Code)
    }
    if got, _ := converged.Load().(string); got != "stale-dynamic-key" {
        t.Fatalf("convergeFn failedKey = %q, want stale-dynamic-key", got)
    }
}

func Test669_Proxy401NoKeyNoConverge(t *testing.T) {
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusUnauthorized)
    }))
    defer upstream.Close()

    var fired atomic.Bool
    p := NewProxy(func(string) (string, string) { return upstream.URL, "" })
    p.SetCredentialConverger(func(moduleID, failedKey string) {
        fired.Store(true)
    })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-design/api/health", nil))
    if w.Code != http.StatusUnauthorized {
        t.Fatalf("401 must pass through, got %d", w.Code)
    }
    if fired.Load() {
        t.Fatal("no injected credential — converge must not fire")
    }
}

func Test669_Proxy200NoConverge(t *testing.T) {
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
    }))
    defer upstream.Close()

    var fired atomic.Bool
    p := NewProxy(func(string) (string, string) { return upstream.URL, "good-key" })
    p.SetCredentialConverger(func(moduleID, failedKey string) {
        fired.Store(true)
    })
    w := httptest.NewRecorder()
    p.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shell/fusion-security/api/v1/ping", nil))
    if w.Code != http.StatusOK {
        t.Fatalf("want 200, got %d", w.Code)
    }
    if fired.Load() {
        t.Fatal("healthy response must not fire convergence")
    }
}
