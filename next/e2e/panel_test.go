package e2e

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// panelCSP is the Content Security Policy core serves the panel with: scripts, styles and
// requests only to core itself, images and fonts also from data: URLs, no forms or embedding,
// and frames only from the extension pages site, which for a panel at 127.0.0.1 is localhost
// on the pages port.
const panelCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self' data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; frame-src http://localhost:"

// isPanelCSP reports whether csp is panelCSP with the pages port filled in.
func isPanelCSP(csp string) bool {
	port, ok := strings.CutPrefix(csp, panelCSP)
	port, slash := strings.CutSuffix(port, "/")
	return ok && slash && regexp.MustCompile(`^[0-9]+$`).MatchString(port)
}

// needPanel skips the test when this checkout's panel is not built (go generate
// ./internal/panel builds it), unless PLUXY_REQUIRE_PANEL is set, as in CI.
func needPanel(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join("..", "internal", "panel", "dist", "index.html")); err == nil {
		return
	}
	if os.Getenv("PLUXY_REQUIRE_PANEL") != "" {
		t.Fatal("the panel is not built: run go generate ./internal/panel")
	}
	t.Skip("the panel is not built: run go generate ./internal/panel")
}

func get(t *testing.T, method, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// Core serves the panel at /panel/ while the management API is on: the page and its assets,
// each under the panel's Content Security Policy, with nothing secret in them.
func TestPanelIsServedWithTheManagementAPI(t *testing.T) {
	needPanel(t)
	base := startCore(t, answeringUpstream(t), func(cfg map[string]any) { cfg["management_key"] = managementKey })

	resp, _ := get(t, "GET", base+"/panel")
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/panel/" {
		t.Fatalf("GET /panel: status %d, Location %q; want 301 to /panel/", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, page := get(t, "GET", base+"/panel/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /panel/: status %d: %s", resp.StatusCode, page)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !isPanelCSP(csp) {
		t.Errorf("GET /panel/: Content-Security-Policy = %q, want %q<pages port>/", csp, panelCSP)
	}
	for h, want := range map[string]string{
		"Content-Type":           "text/html; charset=utf-8",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-cache",
	} {
		if got := resp.Header.Get(h); got != want {
			t.Errorf("GET /panel/: %s = %q, want %q", h, got, want)
		}
	}

	assets := regexp.MustCompile(`(?:src|href)="(/panel/[^"]+)"`).FindAllStringSubmatch(page, -1)
	var js, css bool
	for _, m := range assets {
		resp, body := get(t, "GET", base+m[1])
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", m[1], resp.StatusCode)
			continue
		}
		if !isPanelCSP(resp.Header.Get("Content-Security-Policy")) || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: missing the panel's security headers: %v", m[1], resp.Header)
		}
		if got := resp.Header.Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Errorf("GET %s: Cache-Control %q; assets have content-hashed names", m[1], got)
		}
		switch ct := resp.Header.Get("Content-Type"); {
		case strings.HasSuffix(m[1], ".js"):
			js = ct == "text/javascript; charset=utf-8"
		case strings.HasSuffix(m[1], ".css"):
			css = ct == "text/css; charset=utf-8"
		}
		if strings.Contains(body, managementKey) {
			t.Errorf("GET %s: the asset holds the management key", m[1])
		}
	}
	if !js || !css {
		t.Errorf("the page loads no script (%v) or no stylesheet (%v) served with its type: %s", js, css, page)
	}

	if resp, _ := get(t, "POST", base+"/panel/"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /panel/: status %d, want 405", resp.StatusCode)
	}
	if resp, _ := get(t, "GET", base+"/panel/missing.js"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /panel/missing.js: status %d, want 404", resp.StatusCode)
	}
	// The panel takes only its own path; other paths stay with the client API.
	if resp, _ := get(t, "GET", base+"/panelx"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /panelx: status %d, want 404", resp.StatusCode)
	}
}

// Without a management key the management API is off, and so is the panel.
func TestPanelIsOffWithoutManagementKey(t *testing.T) {
	needPanel(t)
	base := startCore(t, answeringUpstream(t))
	for _, p := range []string{"/panel", "/panel/"} {
		if resp, _ := get(t, "GET", base+p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s without a management key: status %d, want 404", p, resp.StatusCode)
		}
	}
}
