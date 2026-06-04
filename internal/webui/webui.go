// Package webui embeds the built "signalwave" React SPA and serves it under
// /app/ on the same loopback listener as the MCP endpoint. The SPA is a real
// in-browser MCP client that talks to the daemon's "/" streamable-HTTP handler
// (same origin, no CORS).
//
// The build output is committed at internal/webui/dist so `go install` works
// from a clean checkout; see web/ for the Vite source and the `web` make
// target that regenerates dist.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// indexHTML is the SPA entry document, read once from the embedded FS and
// written verbatim for client-routed paths (so the React router can take over
// without a FileServer redirect dance).
var indexHTML []byte

// MountPath is the URL prefix the SPA is served under. Vite is configured with
// base: '/app/' so every asset URL lives below this prefix.
const MountPath = "/app/"

//go:embed all:dist
var embedded embed.FS

// Handler returns an http.Handler that serves the embedded SPA. It expects to
// be mounted at MountPath (e.g. mux.Handle("/app/", webui.Handler())) and
// strips that prefix itself. Unknown paths that are not real asset files fall
// back to index.html so client-side routing works.
func Handler() http.Handler {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		// dist is embedded at build time, so a failure here is a programmer
		// error (bad embed path), not a runtime condition.
		panic("webui: cannot open embedded dist: " + err.Error())
	}
	if b, rerr := fs.ReadFile(dist, "index.html"); rerr == nil {
		indexHTML = b
	}
	fileServer := http.FileServer(http.FS(dist))

	serveIndex := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	}

	return http.StripPrefix(strings.TrimSuffix(MountPath, "/"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upath := strings.TrimPrefix(r.URL.Path, "/")
		if upath == "" {
			// Serve the SPA shell directly so the bare /app/ never triggers the
			// FileServer's index.html -> "./" canonicalisation redirect.
			serveIndex(w)
			return
		}
		if f, ferr := dist.Open(upath); ferr == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback: a path with no file extension that is not a real
		// embedded file is a client-side route — serve the shell so the React
		// router can take over. Missing assets (with an extension) keep 404.
		if path.Ext(upath) == "" {
			serveIndex(w)
			return
		}
		fileServer.ServeHTTP(w, r)
	}))
}
