package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fp = "0123456789abcdef"

func folder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"index.html": "<p>page</p>", "js/app.js": "1", "notes.exe": "x"} {
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
			return Folder{Dir: dir}, true
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
	if w.Code != http.StatusNotFound || w.Header().Get("Content-Security-Policy") != "" {
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
	for _, panel := range []string{"pluxy.example", "192.168.1.2:8400", "evil.localhost:8400"} {
		if got, err := Origin("", panel, 8401); err == nil {
			t.Errorf("Origin for a panel at %s = %q", panel, got)
		}
	}
}
