package server

import (
    "net/http"
    "net/http/httptest"
    "os"
    "path/filepath"
    "strings"
    "testing"

    "github.com/fusion-gateway/fusion-gateway/internal/config"
)

func newShellSPATestServer(t *testing.T, spaDir string) (*Server, *http.ServeMux) {
    t.Helper()
    cfg := &config.ConfigSnapshot{Config: config.DefaultConfig()}
    cfg.Config.Shell.SpaDir = spaDir
    s := &Server{cfg: cfg}
    mux := http.NewServeMux()
    return s, mux
}

func TestShellSPA_ServesIndexAssetsAndFallback(t *testing.T) {
    dir := t.TempDir()
    if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
        t.Fatal(err)
    }
    files := map[string]string{
        "index.html":     "<html>shell-root</html>",
        "assets/app.js":  "console.log(1)",
        "assets/app.css": "body{}",
    }
    for name, content := range files {
        if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
            t.Fatal(err)
        }
    }
    s, mux := newShellSPATestServer(t, dir)
    if !s.registerShellSPA(mux) {
        t.Fatal("expected SPA route registered")
    }

    cases := []struct {
        path         string
        wantBody     string
        wantCache    string
        wantCTPrefix string
    }{
        {"/", "<html>shell-root</html>", "no-store", "text/html"},
        {"/spaces/abc", "<html>shell-root</html>", "no-store", "text/html"},
        {"/login", "<html>shell-root</html>", "no-store", "text/html"},
        {"/assets/app.js", "console.log(1)", "immutable", "text/javascript"},
        {"/assets/app.css", "body{}", "immutable", "text/css"},
    }
    for _, tc := range cases {
        req := httptest.NewRequest(http.MethodGet, tc.path, nil)
        req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
        w := httptest.NewRecorder()
        mux.ServeHTTP(w, req)
        if w.Code != http.StatusOK {
            t.Fatalf("GET %s code = %d, want 200", tc.path, w.Code)
        }
        if got := w.Body.String(); got != tc.wantBody {
            t.Fatalf("GET %s body = %q, want %q", tc.path, got, tc.wantBody)
        }
        if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.wantCTPrefix) {
            t.Fatalf("GET %s content-type = %q, want prefix %q", tc.path, got, tc.wantCTPrefix)
        }
        if got := w.Header().Get("Cache-Control"); !strings.Contains(got, tc.wantCache) {
            t.Fatalf("GET %s cache-control = %q, want contains %q", tc.path, got, tc.wantCache)
        }
    }
}

func TestShellSPA_DirectoryHitFallsBackNoListing(t *testing.T) {
    dir := t.TempDir()
    if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell-root</html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("TOPSECRET"), 0o644); err != nil {
        t.Fatal(err)
    }
    s, mux := newShellSPATestServer(t, dir)
    s.registerShellSPA(mux)

    // "/assets/" is a directory — must serve the SPA fallback, never a listing.
    req := httptest.NewRequest(http.MethodGet, "/assets/", nil)
    req.Header.Set("Accept", "text/html,application/xhtml+xml")
    w := httptest.NewRecorder()
    mux.ServeHTTP(w, req)
    if w.Code != http.StatusOK || w.Body.String() != "<html>shell-root</html>" {
        t.Fatalf("GET /assets/ code = %d body = %q, want index fallback", w.Code, w.Body.String())
    }
    if strings.Contains(w.Body.String(), "TOPSECRET") {
        t.Fatal("directory listing leaked sibling file")
    }
}

func TestShellSPA_TraversalCannotEscapeRoot(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell-root</html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    // Sibling file outside the dist root — must never be reachable.
    outside := filepath.Join(filepath.Dir(dir), "outside-secret.txt")
    if err := os.WriteFile(outside, []byte("OUTSIDE"), 0o644); err != nil {
        t.Fatal(err)
    }
    t.Cleanup(func() { _ = os.Remove(outside) })

    s, mux := newShellSPATestServer(t, dir)
    s.registerShellSPA(mux)

    // ServeMux redirects ".." paths to the cleaned form; the cleaned name
    // misses the dist root and falls back to index.html — never OUTSIDE.
    for _, p := range []string{"/../outside-secret.txt", "/%2e%2e/outside-secret.txt"} {
        req := httptest.NewRequest(http.MethodGet, p, nil)
        w := httptest.NewRecorder()
        mux.ServeHTTP(w, req)
        if strings.Contains(w.Body.String(), "OUTSIDE") {
            t.Fatalf("GET %s escaped dist root: %q", p, w.Body.String())
        }
    }
}

func TestShellSPA_EmptyDirNotRegistered(t *testing.T) {
    s, mux := newShellSPATestServer(t, "")
    if s.registerShellSPA(mux) {
        t.Fatal("empty spa_dir must not register a route")
    }
    w := httptest.NewRecorder()
    mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
    if w.Code != http.StatusNotFound {
        t.Fatalf("GET / code = %d, want 404 (previous behavior)", w.Code)
    }
}

func TestShellSPA_MissingIndexWarnsAndSkips(t *testing.T) {
    dir := t.TempDir() // exists but no index.html
    s, mux := newShellSPATestServer(t, dir)
    if s.registerShellSPA(mux) {
        t.Fatal("spa_dir without index.html must not register a route")
    }
    w := httptest.NewRecorder()
    mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
    if w.Code != http.StatusNotFound {
        t.Fatalf("GET / code = %d, want 404", w.Code)
    }
}

func TestShellSPA_APINamespacesWinOverRoot(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell-root</html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    s, mux := newShellSPATestServer(t, dir)
    s.registerShellSPA(mux)
    mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("api"))
    })
    mux.HandleFunc("/shell/", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("proxy"))
    })
    mux.HandleFunc("/admin/", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("admin"))
    })
    mux.HandleFunc("/api/v1/shell/me", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("me"))
    })
    mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
        _, _ = w.Write([]byte("ok"))
    })

    cases := map[string]string{
        "/v1/chat/completions": "api",
        "/shell/fusion-mlx/":   "proxy",
        "/admin/anything":      "admin",
        "/api/v1/shell/me":     "me",
        "/health":              "ok",
    }
    for p, want := range cases {
        w := httptest.NewRecorder()
        mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
        if w.Body.String() != want {
            t.Fatalf("GET %s body = %q, want %q (namespace lost to SPA)", p, w.Body.String(), want)
        }
    }
}

func TestShellSPA_APIShapedMissGets404JSON(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell-root</html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    s, mux := newShellSPATestServer(t, dir)
    s.registerShellSPA(mux)

    cases := []struct {
        name   string
        method string
        accept string
    }{
        {"post-typo-route", http.MethodPost, ""},
        {"put-typo-route", http.MethodPut, "application/json"},
        {"get-json-accept", http.MethodGet, "application/json"},
        {"get-wildcard-accept", http.MethodGet, "*/*"},
        {"get-no-accept", http.MethodGet, ""},
    }
    for _, tc := range cases {
        req := httptest.NewRequest(tc.method, "/v1/rpc", nil)
        if tc.accept != "" {
            req.Header.Set("Accept", tc.accept)
        }
        w := httptest.NewRecorder()
        mux.ServeHTTP(w, req)
        if w.Code != http.StatusNotFound {
            t.Fatalf("%s: %s /v1/rpc code = %d, want 404", tc.name, tc.method, w.Code)
        }
        if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
            t.Fatalf("%s: content-type = %q, want application/json (HTML masks 404s)", tc.name, got)
        }
        if !strings.Contains(w.Body.String(), `"code":"not_found"`) {
            t.Fatalf("%s: body = %q, want standardized envelope", tc.name, w.Body.String())
        }
        if strings.Contains(w.Body.String(), "doctype") {
            t.Fatalf("%s: HTML leaked into API-shaped miss", tc.name)
        }
    }
}

func TestShellSPA_NavigationFallbackStillWorks(t *testing.T) {
    dir := t.TempDir()
    if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell-root</html>"), 0o644); err != nil {
        t.Fatal(err)
    }
    s, mux := newShellSPATestServer(t, dir)
    s.registerShellSPA(mux)

    // GET navigation: full fallback.
    req := httptest.NewRequest(http.MethodGet, "/spaces/abc", nil)
    req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
    w := httptest.NewRecorder()
    mux.ServeHTTP(w, req)
    if w.Code != http.StatusOK || w.Body.String() != "<html>shell-root</html>" {
        t.Fatalf("GET deep link code = %d body = %q, want index fallback", w.Code, w.Body.String())
    }

    // HEAD navigation: fallback without a body.
    req = httptest.NewRequest(http.MethodHead, "/login", nil)
    req.Header.Set("Accept", "text/html")
    w = httptest.NewRecorder()
    mux.ServeHTTP(w, req)
    if w.Code != http.StatusOK {
        t.Fatalf("HEAD deep link code = %d, want 200", w.Code)
    }

    // Real file miss-free path: asset fetch with a non-html Accept still serves.
    if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
        t.Fatal(err)
    }
    req = httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
    req.Header.Set("Accept", "*/*")
    w = httptest.NewRecorder()
    mux.ServeHTTP(w, req)
    if w.Code != http.StatusOK || w.Body.String() != "console.log(1)" {
        t.Fatalf("asset fetch code = %d body = %q, want served file", w.Code, w.Body.String())
    }
}
