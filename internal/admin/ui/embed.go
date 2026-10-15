package ui

import (
    "embed"
    "encoding/json"
    "io/fs"
    "log"
    "net/http"
    "strings"
)

//go:embed dist/*
var distFS embed.FS

// AdminManifest mirrors the PRD §5.1 manifest schema fields the gateway
// needs to auto-register the built-in admin module (#216). The file lives at
// web/admin/public/manifest.json (vite copies it into dist) so the frontend
// and the gateway auto-registration share one source of truth.
type AdminManifest struct {
    ID       string   `json:"id"`
    Title    string   `json:"title"`
    TitleEn  string   `json:"title_en"`
    Version  string   `json:"version"`
    Path     string   `json:"path"`
    Entry    string   `json:"entry"`
    Health   string   `json:"health"`
    Backend  string   `json:"backend"`
    Devices  []string `json:"devices"`
    Category string   `json:"category"`
}

// Manifest parses the embedded dist/manifest.json. ok=false when the file is
// missing or corrupt — the caller logs and skips auto-registration rather
// than failing the gateway (the manifest is an optimization, not a
// dependency; shell still works via manual/static registration).
func Manifest() (AdminManifest, bool) {
    raw, err := distFS.ReadFile("dist/manifest.json")
    if err != nil {
        return AdminManifest{}, false
    }
    var m AdminManifest
    if err := json.Unmarshal(raw, &m); err != nil || m.ID == "" {
        return AdminManifest{}, false
    }
    return m, true
}

func Handler() http.Handler {
    sub, err := fs.Sub(distFS, "dist")
    if err != nil {
        // Unreachable in practice: fs.Sub over a compile-time //go:embed dist/*
        // subtree cannot fail — "dist" is a guaranteed-present root. But a bare
        // panic on the admin SPA path is the wrong failure mode for a production
        // gateway: a future dist/ rename would crash the whole process on every
        // admin request instead of serving a logged 500. Fail loudly, do not
        // crash the process.
        log.Printf("ui: embedded dist subtree missing — admin SPA unavailable: %v", err)
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            http.Error(w, `{"error":{"message":"admin UI unavailable: embedded assets missing","type":"server_error"}}`, http.StatusInternalServerError)
        })
    }
    fileServer := http.FileServer(http.FS(sub))
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        path := r.URL.Path
        if path != "" && !strings.HasPrefix(path, "/assets/") {
            if _, err := fs.Stat(sub, strings.TrimPrefix(path, "/")); err != nil {
                r.URL.Path = "/"
            }
        }
        fileServer.ServeHTTP(w, r)
    })
}
