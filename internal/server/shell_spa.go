package server

import (
    "fmt"
    "io/fs"
    "log/slog"
    "net/http"
    "os"
    "path"
    "strings"
)

// shellSPAHandler serves the union-web shell SPA dist directory (#247) as the
// gateway root: real files pass through, everything else falls back to
// index.html so SPA deep links (/spaces/<id>, /login ...) survive a refresh.
// Directory hits are treated as misses — no directory listings, the fallback
// page is served instead. The "/" route is a ServeMux catch-all, so
// longest-pattern precedence keeps every API namespace (/v1/*, /shell/,
// /admin/, /api/v1/shell/*, /health ...) ahead of it; unmatched paths reach
// the SPA. Unauthenticated on purpose: the SPA's first screen is the shell
// login (same posture as the embedded /admin/ SPA); its API calls carry
// their own session/key auth.
type shellSPAHandler struct {
    root       fs.FS
    fileServer http.Handler
}

// newShellSPAHandler validates the dist directory up front — registration
// without an index.html would turn every unmatched path into a confusing
// fallback loop, so the caller gets an error and logs a WARN instead.
func newShellSPAHandler(dir string) (*shellSPAHandler, error) {
    root := os.DirFS(dir)
    if _, err := fs.Stat(root, "index.html"); err != nil {
        return nil, fmt.Errorf("spa_dir %q has no index.html: %w", dir, err)
    }
    return &shellSPAHandler{
        root:       root,
        fileServer: http.FileServer(http.FS(root)),
    }, nil
}

func (h *shellSPAHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
    if name != "" {
        if st, err := fs.Stat(h.root, name); err != nil || st.IsDir() {
            r.URL.Path = "/"
        }
    }
    switch {
    case r.URL.Path == "/":
        w.Header().Set("Cache-Control", "no-store")
    case strings.HasPrefix(r.URL.Path, "/assets/"):
        w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
    }
    h.fileServer.ServeHTTP(w, r)
}

// registerShellSPA wires the "/" SPA route when web_shell.spa_dir is set.
// Returns false (and logs a WARN) when the dir is unusable — the gateway
// keeps its previous 404-on-unmatched behavior and the shell API is
// unaffected. Registration happens after every other route on purpose:
// ServeMux precedence is longest-pattern-wins regardless of order, but a
// last-position read keeps the catch-all obvious.
func (s *Server) registerShellSPA(mux *http.ServeMux) bool {
    dir := strings.TrimSpace(s.cfg.Config.Shell.SpaDir)
    if dir == "" {
        return false
    }
    spa, err := newShellSPAHandler(dir)
    if err != nil {
        slog.Warn("web_shell.spa_dir unusable — gateway root keeps 404 behavior",
            "dir", dir, "error", err)
        return false
    }
    mux.Handle("/", spa)
    slog.Info("union-web shell SPA hosted at gateway root",
        "dir", dir, "namespaces_reserved", "/v1/*,/shell/*,/admin/*,/api/*,/gateway/*,/health*")
    return true
}
