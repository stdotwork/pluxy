// Package panel serves the web panel core ships with: the browser build of the UI kit in
// next/ui, embedded in core's binary. It is built by go generate, not kept in git:
//
//	go generate ./internal/panel
//
// runs the UI's build (it needs Bun), which writes the files into dist. A core built without
// them serves an explanation in place of the panel.
package panel

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:generate sh -c "cd ../../ui && bun install --frozen-lockfile && bun run build"

//go:embed all:dist
var dist embed.FS

// Prefix is where the panel is served.
const Prefix = "/panel/"

// CSP is the Content Security Policy of every panel response. Scripts, styles and requests go
// only to core itself; images and fonts may also be data: URLs, so a theme's custom CSS can
// embed them but cannot load anything from elsewhere. No frames, forms or embedding in other
// sites.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self' data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// NotBuilt is the reply of a core built without its panel.
const NotBuilt = "This core was built without its panel. Build the panel with go generate ./internal/panel, then build core again.\n"

// Assets returns the embedded panel files.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the folder
	}
	return sub
}

// Built reports whether assets hold a built panel.
func Built(assets fs.FS) bool {
	_, err := fs.Stat(assets, "index.html")
	return err == nil
}

var types = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".woff2": "font/woff2",
}

type file struct {
	body []byte
	etag string
	kind string
}

// Handler serves the panel files in assets at Prefix while on returns true (core passes whether
// the management API is on); otherwise every panel path is not found. Register it for both
// "/panel" and Prefix. The page is never cached without asking core; the other files have
// content-hashed names, so browsers keep them.
func Handler(assets fs.FS, on func() bool) http.Handler {
	files := map[string]file{}
	_ = fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			return nil
		}
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			return nil
		}
		kind, ok := types[path.Ext(name)]
		if !ok {
			kind = "application/octet-stream"
		}
		sum := sha256.Sum256(body)
		files[name] = file{body: body, etag: `"` + hex.EncodeToString(sum[:12]) + `"`, kind: kind}
		return nil
	})
	_, built := files["index.html"]

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !on() {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == strings.TrimSuffix(Prefix, "/") {
			http.Redirect(w, r, Prefix, http.StatusMovedPermanently)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", CSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		if !built {
			h.Set("Cache-Control", "no-store")
			http.Error(w, NotBuilt, http.StatusServiceUnavailable)
			return
		}
		name, ok := strings.CutPrefix(r.URL.Path, Prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name == "" {
			name = "index.html"
		}
		f, ok := files[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", f.kind)
		h.Set("ETag", f.etag)
		if name == "index.html" {
			h.Set("Cache-Control", "no-cache")
		} else {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
	})
}
