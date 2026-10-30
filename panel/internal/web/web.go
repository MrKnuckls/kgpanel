// Package web embeds the panel UI (html/css/js + brand assets) into the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// UI returns the embedded static file server for the frontend.
func UI() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// Page returns the raw bytes of an embedded page (e.g. "index.html") so the
// handler can gate / between login and dashboard without a fragile HTTP
// round-trip. Returns nil if the page doesn't exist.
func Page(name string) []byte {
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		return nil
	}
	return b
}
