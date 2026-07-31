package ui

import (
    "embed"
    "io/fs"
    "net/http"
    "strings"
)

//go:embed dist/*
var distFS embed.FS

func Handler() http.Handler {
    sub, err := fs.Sub(distFS, "dist")
    if err != nil {
        panic(err)
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
