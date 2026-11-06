package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writePackage writes a package folder with a manifest made of base plus extra, a program and the
// ui files given, and returns the manifest's path.
func writePackage(t *testing.T, extra map[string]any, ui map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	m := map[string]any{
		"manifest_version": 0, "id": "pub/ext", "version": "1.0.0", "protocol": []int{0},
		"programs": map[string]string{runtime.GOOS + "/" + runtime.GOARCH: "prog"},
	}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prog"), []byte("program"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range ui {
		p := filepath.Join(dir, "ui", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, ManifestFile)
}

func pagesUI(extra map[string]any) map[string]any {
	ui := map[string]any{
		"version": 0, "dir": "ui", "permissions": []string{"config.read"},
		"pages": []any{map[string]any{"id": "main", "title": "Main", "short": "Main", "path": "index.html"}},
	}
	for k, v := range extra {
		ui[k] = v
	}
	return ui
}

func TestManifestReadsPagesAndSettings(t *testing.T) {
	path := writePackage(t, map[string]any{
		"ui": pagesUI(nil),
		"config": map[string]any{"version": 0, "schema": map[string]any{
			"type": "object", "properties": map[string]any{"a": map[string]any{"type": "boolean"}},
		}},
	}, map[string]string{"index.html": "<p>hi</p>"})
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.UI == nil || m.UI.Pages[0].ID != "main" || m.UIDir() != filepath.Join(filepath.Dir(path), "ui") {
		t.Fatalf("ui %+v at %s", m.UI, m.UIDir())
	}
	if s := m.Settings(); s == nil || len(s.Fields) != 1 {
		t.Fatalf("settings %+v", s)
	}
}

func TestManifestRefusesBadPagesAndSettings(t *testing.T) {
	page := func(p map[string]any) map[string]any {
		return pagesUI(map[string]any{"pages": []any{p}})
	}
	for name, extra := range map[string]map[string]any{
		"ui version":         {"ui": pagesUI(map[string]any{"version": 1})},
		"ui dir escapes":     {"ui": pagesUI(map[string]any{"dir": "../x"})},
		"ui dir absolute":    {"ui": pagesUI(map[string]any{"dir": "/tmp"})},
		"unknown permission": {"ui": pagesUI(map[string]any{"permissions": []string{"management"}})},
		"no pages":           {"ui": pagesUI(map[string]any{"pages": []any{}})},
		"bad page id":        {"ui": page(map[string]any{"id": "A/b", "title": "T", "short": "S", "path": "index.html"})},
		"page not html":      {"ui": page(map[string]any{"id": "a", "title": "T", "short": "S", "path": "index.js"})},
		"page escapes":       {"ui": page(map[string]any{"id": "a", "title": "T", "short": "S", "path": "../index.html"})},
		"short too long":     {"ui": page(map[string]any{"id": "a", "title": "T", "short": "123456789", "path": "index.html"})},
		"duplicate page":     {"ui": pagesUI(map[string]any{"pages": []any{map[string]any{"id": "a", "title": "T", "short": "S", "path": "index.html"}, map[string]any{"id": "a", "title": "T", "short": "S", "path": "index.html"}}})},
		"config version":     {"config": map[string]any{"version": 1, "schema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "boolean"}}}}},
		"config keyword":     {"config": map[string]any{"version": 0, "schema": map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "boolean", "x-anything": 1}}}}},
	} {
		if _, err := LoadManifest(writePackage(t, extra, map[string]string{"index.html": "x"})); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFingerprintCoversPageFiles(t *testing.T) {
	path := writePackage(t, map[string]any{"ui": pagesUI(nil)}, map[string]string{"index.html": "one", "js/app.js": "a"})
	before, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "ui", "js", "app.js"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("changing a page file left the fingerprint as it was")
	}
	// A file renamed with the same contents changes it too.
	if err := os.Rename(filepath.Join(filepath.Dir(path), "ui", "js", "app.js"), filepath.Join(filepath.Dir(path), "ui", "js", "other.js")); err != nil {
		t.Fatal(err)
	}
	if renamed, _ := Fingerprint(path); renamed == after {
		t.Fatal("renaming a page file left the fingerprint as it was")
	}
}

func TestFingerprintRefusesLinksAndMissingPages(t *testing.T) {
	path := writePackage(t, map[string]any{"ui": pagesUI(nil)}, map[string]string{"index.html": "x"})
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(filepath.Dir(path), "ui", "link.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(path); err == nil || !strings.Contains(err.Error(), "regular files") {
		t.Fatalf("a link in the ui folder: %v", err)
	}

	missing := writePackage(t, map[string]any{"ui": pagesUI(nil)}, map[string]string{"other.html": "x"})
	if _, err := Fingerprint(missing); err == nil || !strings.Contains(err.Error(), "not in the ui folder") {
		t.Fatalf("a page whose file is missing: %v", err)
	}
}
