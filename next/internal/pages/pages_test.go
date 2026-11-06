package pages

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fp = "0123456789abcdef"

var files = map[string]string{"index.html": "<p>page</p>", "js/app.js": "1", "notes.exe": "x"}

// digests returns the SHA-256 of files, as host records them, plus one for link.html, a file
// that is a link.
func digests() map[string]string {
	out := map[string]string{}
	for name, body := range files {
		sum := sha256.Sum256([]byte(body))
		out[name] = hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256([]byte("secret"))
	out["link.html"] = hex.EncodeToString(sum[:])
	return out
}

func folder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func handler(dir string, on bool) http.Handler {
	return Handler(func(id, key string) (Folder, bool) {
		if id == "pub/ext" && key == fp {
			return Folder{Dir: dir, Files: digests()}, true
		}
		return Folder{}, false
	}, func() bool { return on })
}

func get(h http.Handler, method, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "localhost:8401"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServesPageFilesSandboxed(t *testing.T) {
	h := handler(folder(t), true)
	for path, kind := range map[string]string{
		"/pub/ext/" + fp + "/index.html": "text/html; charset=utf-8",
		"/pub/ext/" + fp + "/js/app.js":  "text/javascript; charset=utf-8",
	} {
		w := get(h, http.MethodGet, path)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != kind {
			t.Fatalf("%s: %d %q", path, w.Code, w.Header().Get("Content-Type"))
		}
		csp := w.Header().Get("Content-Security-Policy")
		for _, want := range []string{
			"sandbox allow-scripts;", "default-src 'none'", "script-src http://localhost:8401/pub/ext/" + fp + "/;",
			"connect-src 'none'", "frame-src 'none'", "form-action 'none'", "base-uri 'none'", "worker-src 'none'",
		} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: policy %q lacks %q", path, csp, want)
			}
		}
		if strings.Contains(csp, "allow-same-origin") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: policy %q", path, csp)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("X-Content-Type-Options") != "nosniff" ||
			w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("%s: headers %v", path, w.Header())
		}
	}
}

func TestServesNothingElse(t *testing.T) {
	dir := folder(t)
	outside := filepath.Join(filepath.Dir(dir), "outside.html")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.html")); err != nil {
		t.Fatal(err)
	}
	h := handler(dir, true)
	for _, path := range []string{
		"/", "/pub/ext/" + fp, "/pub/ext/" + fp + "/", "/pub/ext/ffffffffffffffff/index.html", "/pub/other/" + fp + "/index.html",
		"/pub/ext/" + fp + "/notes.exe", "/pub/ext/" + fp + "/../outside.html", "/pub/ext/" + fp + "/js/../../outside.html",
		"/pub/ext/" + fp + "/link.html", "/pub/ext/" + fp + "/js", "/pub/ext/" + fp + "/missing.html",
	} {
		if w := get(h, http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	if w := get(h, http.MethodPost, "/pub/ext/"+fp+"/index.html"); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
	if w := get(handler(dir, false), http.MethodGet, "/pub/ext/"+fp+"/index.html"); w.Code != http.StatusNotFound {
		t.Errorf("with management off: %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodGet, "/pub/ext/"+fp+"/index.html", nil)
	r.Host = "evil; script-src *"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Security-Policy") != errorCSP {
		t.Errorf("a Host that would change the policy: %d %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
}

func TestOriginIsAnotherSite(t *testing.T) {
	for _, c := range []struct{ configured, panel, want string }{
		{"", "127.0.0.1:8400", "http://localhost:8401"},
		{"", "localhost:8400", "http://127.0.0.1:8401"},
		{"", "[::1]:8400", "http://localhost:8401"},
		{"", "LOCALHOST", "http://127.0.0.1:8401"},
		{"https://pages.example", "pluxy.example", "https://pages.example"},
	} {
		got, err := Origin(c.configured, c.panel, 8401)
		if err != nil || got != c.want {
			t.Errorf("Origin(%q, %q) = %q, %v; want %q", c.configured, c.panel, got, err, c.want)
		}
	}
	// Review finding 1: a configured pages origin on the panel's own site is refused.
	for _, c := range []struct{ configured, panel string }{
		{"https://pages.example.com", "panel.example.com"},
		{"https://example.com", "panel.example.com:8400"},
		{"http://127.0.0.1:9000", "127.0.0.1:8400"},
	} {
		if got, err := Origin(c.configured, c.panel, 8401); err == nil {
			t.Errorf("Origin(%q) for a panel at %s = %q, want refused", c.configured, c.panel, got)
		}
	}
	for _, panel := range []string{"pluxy.example", "192.168.1.2:8400", "evil.localhost:8400"} {
		if got, err := Origin("", panel, 8401); err == nil {
			t.Errorf("Origin for a panel at %s = %q", panel, got)
		}
	}
}

// Review finding 6: a file changed, or added, after the extension's files were checked is not
// served. Review finding 9: replies that are not page files carry a restrictive policy too.
func TestServesOnlyTheFilesAsChecked(t *testing.T) {
	dir := folder(t)
	h := handler(dir, true)
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("changed after fingerprint"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "added.html"), []byte("added later"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/pub/ext/" + fp + "/index.html", "/pub/ext/" + fp + "/added.html"} {
		if w := get(h, http.MethodGet, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	if w := get(h, http.MethodGet, "/pub/ext/"+fp+"/js/app.js"); w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
		t.Errorf("an unchanged file: %d, ETag %q", w.Code, w.Header().Get("ETag"))
	}
	for _, path := range []string{"/missing", "/pub/ext/" + fp + "/index.html"} {
		w := get(h, http.MethodGet, path)
		if csp := w.Header().Get("Content-Security-Policy"); csp != "sandbox; default-src 'none'" {
			t.Errorf("%s (%d): policy %q", path, w.Code, csp)
		}
	}
}
