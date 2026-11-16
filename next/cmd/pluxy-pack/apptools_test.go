package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// appToolJSON is a valid app tool manifest but for these platform, program and app.ui.dir.
func appToolJSON(t *testing.T, platform, program, uiDir string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"manifest_version": 1, "id": "pluxy/t", "version": "0.1.0",
		"app": map[string]any{
			"tier":     "native",
			"programs": map[string]string{platform: program},
			"ui":       map[string]any{"version": 0, "dir": uiDir},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An app tool's program paths and app.ui.dir are refused as the app refuses them (store_path):
// an empty, `.` or `..` part, a backslash or a `:`; a platform must be exactly GOOS/GOARCH.
func TestAppManifestValidateRefusesWhatTheAppRefuses(t *testing.T) {
	check := func(platform, program, uiDir string) error {
		t.Helper()
		var m appManifest
		if err := json.Unmarshal(appToolJSON(t, platform, program, uiDir), &m); err != nil {
			t.Fatal(err)
		}
		return m.validate()
	}
	if err := check("linux/amd64", "bin/t", "pages"); err != nil {
		t.Fatalf("a valid manifest: %v", err)
	}
	if err := check("windows/amd64", "bin/t.exe", "pages/ui"); err != nil {
		t.Fatalf("a valid Windows manifest: %v", err)
	}
	bad := []string{"", "a/./b", "a/../b", "./a", "../a", "a/", "/a", "a//b", ".", "..", `a\b`, "C:a", "a:b", "a\x00b"}
	for _, p := range bad {
		if err := check("linux/amd64", p, "pages"); err == nil || !strings.Contains(err.Error(), "program for linux/amd64") {
			t.Errorf("program %q: %v, want refused", p, err)
		}
		if err := check("linux/amd64", "bin/t", p); err == nil || !strings.Contains(err.Error(), "app.ui.dir") {
			t.Errorf("app.ui.dir %q: %v, want refused", p, err)
		}
	}
	for _, platform := range []string{"linux/amd64/v2", "linux", "/amd64", "linux/", "linux//amd64"} {
		if err := check(platform, "bin/t", "pages"); err == nil || !strings.Contains(err.Error(), "GOOS/GOARCH") {
			t.Errorf("platform %q: %v, want refused", platform, err)
		}
	}
}
