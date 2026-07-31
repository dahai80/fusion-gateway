package ui

import (
    "embed"
    "io/fs"
    "net/http"
)

//go:embed dist/*
var distFS embed.FS

func Handler() http.Handler {
    sub, err := fs.Sub(distFS, "dist")
    if err != nil {
        panic(err)
    }
    return http.FileServer(http.FS(sub))
}
