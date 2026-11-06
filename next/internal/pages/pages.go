// Package pages serves extension pages on their own site (next/ui/extension-pages/README.md):
// the files of the ui folders of the extensions that run, each with a Content Security Policy
// that sandboxes the document and lets it load only its own folder's files.
package pages

import (
	"errors"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// hostHeader is what a Host may hold here: it goes into the policy, so nothing that could end a
// source expression.
var hostHeader = regexp.MustCompile(`^[A-Za-z0-9.:\[\]-]{1,255}$`)

// Folder is the ui folder of an extension that runs now, as the pages site serves it.
type Folder struct {
	// Dir is the folder on disk.
	Dir string
}

// Lookup returns the ui folder of extension id whose fingerprint key is fp, when one runs.
type Lookup func(id, fp string) (Folder, bool)

// FingerprintKey is the part of an extension generation's fingerprint the pages site's
// addresses carry.
func FingerprintKey(fingerprint string) string {
	k := strings.TrimPrefix(fingerprint, "sha256:")
	if len(k) > 16 {
		k = k[:16]
	}
	return k
}

// Path returns the address of a file in extension id's ui folder on the pages site, without
// the origin.
func Path(id, fingerprint, file string) string {
	return "/" + id + "/" + FingerprintKey(fingerprint) + "/" + file
}

var types = map[string]string{
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".json":  "application/json",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".webp":  "image/webp",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".txt":   "text/plain; charset=utf-8",
}

// CSP returns the Content Security Policy of a file served from folder (an absolute URL ending
// in "/"): sandboxed with scripts only, loading only the folder's files, and no requests from
// script, frames, workers or forms.
func CSP(folder string) string {
	return "sandbox allow-scripts; default-src 'none'; script-src " + folder + "; style-src " + folder +
		"; img-src " + folder + " data: blob:; font-src " + folder + " data:; media-src " + folder +
		" data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; manifest-src 'none'; " +
		"form-action 'none'; base-uri 'none'"
}

// Handler serves the pages site while on returns true (core passes whether the management API
// is on); otherwise everything is not found. Addresses are /{publisher}/{name}/{fp}/{file},
// for an extension lookup finds.
func Handler(lookup Lookup, on func() bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if !on() || !hostHeader.MatchString(r.Host) {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)
		if len(parts) != 4 || parts[3] == "" {
			http.NotFound(w, r)
			return
		}
		id, fp, file := parts[0]+"/"+parts[1], parts[2], parts[3]
		kind, ok := types[strings.ToLower(path.Ext(file))]
		if !ok || !fs.ValidPath(file) {
			http.NotFound(w, r)
			return
		}
		folder, ok := lookup(id, fp)
		if !ok {
			http.NotFound(w, r)
			return
		}
		root, err := os.OpenRoot(folder.Dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		f, err := root.Open(file) // cannot leave the folder, through links or otherwise
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		dir := "/" + id + "/" + fp + "/"
		h.Set("Content-Security-Policy", CSP(origin(r)+dir))
		// A sandboxed page has an opaque origin, and browsers fetch its module scripts and fonts
		// with CORS. The files are public; no request here carries or needs a credential.
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Content-Type", kind)
		h.Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "", info.ModTime(), f)
	})
}

// origin is the pages site's origin as this request reached it.
func origin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// Origin returns the pages site's origin for a panel reached at panelHost (a request's Host),
// with the pages listener on port: configured when set; else, for a panel on a loopback name,
// the other loopback name, so the pages are on another site than the panel. It returns why
// there is none otherwise.
func Origin(configured, panelHost string, port int) (string, error) {
	if configured != "" {
		return configured, nil
	}
	host := panelHost
	if h, _, err := net.SplitHostPort(panelHost); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	var other string
	switch host {
	case "127.0.0.1", "::1":
		other = "localhost"
	case "localhost":
		other = "127.0.0.1"
	default:
		return "", errors.New("extension pages need extension_pages.url in core's config when core is reached by a name other than 127.0.0.1 or localhost")
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(other, strconv.Itoa(port))}).String(), nil
}
