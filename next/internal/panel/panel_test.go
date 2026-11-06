package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var built = fstest.MapFS{
	"index.html":       {Data: []byte(`<script type="module" src="/panel/app-1a2b.js"></script>`)},
	"app-1a2b.js":      {Data: []byte(`console.log(1)`)},
	"app-1a2b.css":     {Data: []byte(`body{}`)},
	".gitkeep":         {Data: nil},
	"fonts/x-9f.woff2": {Data: []byte{0, 1}},
}

func serve(t *testing.T, h http.Handler, method, target string, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func on() bool { return true }

func TestServesFilesWithTheirTypesAndCaching(t *testing.T) {
	h := Handler(built, on)
	for _, c := range []struct{ path, kind, cache string }{
		{"/panel/", "text/html; charset=utf-8", "no-cache"},
		{"/panel/index.html", "text/html; charset=utf-8", "no-cache"},
		{"/panel/app-1a2b.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/panel/app-1a2b.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/panel/fonts/x-9f.woff2", "font/woff2", "public, max-age=31536000, immutable"},
	} {
		w := serve(t, h, "GET", c.path)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s: status %d", c.path, w.Code)
			continue
		}
		for k, want := range map[string]string{"Content-Type": c.kind, "Cache-Control": c.cache, "Content-Security-Policy": CSP,
			"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cross-Origin-Resource-Policy": "same-origin"} {
			if got := w.Header().Get(k); got != want {
				t.Errorf("GET %s: %s = %q, want %q", c.path, k, got, want)
			}
		}
	}
}

func TestRevalidatesWithETag(t *testing.T) {
	h := Handler(built, on)
	etag := serve(t, h, "GET", "/panel/").Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if w := serve(t, h, "GET", "/panel/", "If-None-Match", etag); w.Code != http.StatusNotModified {
		t.Errorf("If-None-Match with the current ETag: status %d, want 304", w.Code)
	}
	if w := serve(t, h, "HEAD", "/panel/app-1a2b.js"); w.Code != http.StatusOK || w.Body.Len() != 0 {
		t.Errorf("HEAD: status %d, %d body bytes", w.Code, w.Body.Len())
	}
}

func TestRefusesWhatItDoesNotServe(t *testing.T) {
	h := Handler(built, on)
	for _, c := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/panel/.gitkeep", http.StatusNotFound},
		{"GET", "/panel/missing.js", http.StatusNotFound},
		{"GET", "/panel/../panel.go", http.StatusNotFound},
		{"GET", "/panelx", http.StatusNotFound},
		{"POST", "/panel/", http.StatusMethodNotAllowed},
		{"DELETE", "/panel/app-1a2b.js", http.StatusMethodNotAllowed},
	} {
		if w := serve(t, h, c.method, c.path); w.Code != c.code {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, w.Code, c.code)
		}
	}
	if w := serve(t, h, "GET", "/panel"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != Prefix {
		t.Errorf("GET /panel: status %d to %q, want 301 to %s", w.Code, w.Header().Get("Location"), Prefix)
	}
}

// While the management API is off, the panel's paths are not found, as the API's are.
func TestOffMeansNotFound(t *testing.T) {
	h := Handler(built, func() bool { return false })
	for _, p := range []string{"/panel", "/panel/", "/panel/app-1a2b.js"} {
		if w := serve(t, h, "GET", p); w.Code != http.StatusNotFound {
			t.Errorf("GET %s while off: status %d, want 404", p, w.Code)
		}
	}
}

// A core built without the panel says so and how to build it, instead of a blank page.
func TestNotBuiltExplains(t *testing.T) {
	h := Handler(fstest.MapFS{".gitkeep": {}}, on)
	w := serve(t, h, "GET", "/panel/")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "go generate ./internal/panel") {
		t.Errorf("unbuilt panel: status %d: %s", w.Code, w.Body)
	}
	if w.Header().Get("Content-Security-Policy") != CSP {
		t.Error("unbuilt panel reply lacks the CSP")
	}
	if Built(fstest.MapFS{".gitkeep": {}}) || !Built(built) {
		t.Error("Built misreports")
	}
}

// The embedded folder always exists, built or not, so core builds without the UI toolchain.
func TestAssetsAlwaysOpen(t *testing.T) {
	_ = Assets()
}
