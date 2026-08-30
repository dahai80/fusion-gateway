package ui

import (
    "embed"
    "io/fs"
    "log"
    "net/http"
    "strings"
)

//go:embed dist/*
var distFS embed.FS

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
