package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var staticFS embed.FS

func (s *Server) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		f, err := sub.Open(path)
		if err != nil {
			// SPA fallback: serve index.html for non-API routes.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})
}
