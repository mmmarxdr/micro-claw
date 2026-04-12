package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var staticFS embed.FS

// hasFrontendAssets reports whether the embedded filesystem contains
// a built frontend (JS/CSS bundles in assets/).
func hasFrontendAssets() bool {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return false
	}
	entries, err := fs.ReadDir(sub, "assets")
	return err == nil && len(entries) > 0
}

func (s *Server) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")

	if !hasFrontendAssets() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fallbackHTML))
		})
	}

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

const fallbackHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8"/>
<meta name="viewport" content="width=device-width,initial-scale=1.0"/>
<title>microagent</title>
<style>
*{margin:0;padding:0;box-sizing:border-box}
body{background:#0a0a0a;color:#e5e5e5;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,monospace;display:flex;align-items:center;justify-content:center;min-height:100vh}
.c{max-width:520px;padding:2rem;text-align:center}
h1{font-size:1.5rem;font-weight:600;margin-bottom:.75rem;color:#fff}
p{font-size:.9rem;line-height:1.6;color:#a3a3a3;margin-bottom:1rem}
code{background:#1a1a1a;border:1px solid #262626;border-radius:4px;padding:2px 6px;font-size:.85rem;color:#d4d4d4}
.hint{font-size:.8rem;color:#525252;margin-top:1.5rem}
.api{display:inline-block;margin-top:1rem;color:#3b82f6;text-decoration:none;font-size:.85rem}
.api:hover{text-decoration:underline}
</style>
</head>
<body>
<div class="c">
<h1>Web UI not installed</h1>
<p>The REST API is running — only the frontend assets are missing.</p>
<p>Install them with:</p>
<p><code>make frontend</code></p>
<p>Then rebuild:</p>
<p><code>make build</code></p>
<p class="hint">Release binaries from GitHub Releases include the frontend by default.<br/>
This message only appears when building from source without assets.</p>
<a class="api" href="/api/status">/api/status &rarr;</a>
</div>
</body>
</html>`
