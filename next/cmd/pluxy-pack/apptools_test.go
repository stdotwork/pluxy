package main

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
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

// appToolsTree makes a root with app-tools/<name>/pluxy-extension.json for each tool (its
// program for linux/amd64), and a programs folder with each program built.
func appToolsTree(t *testing.T, tools map[string]string) (root, programs string) {
	t.Helper()
	root, programs = t.TempDir(), t.TempDir()
	for name, program := range tools {
		b, err := json.Marshal(map[string]any{
			"manifest_version": 1, "id": "pluxy/" + name, "version": "0.1.0",
			"app": map[string]any{"tier": "native", "programs": map[string]string{"linux/amd64": program}},
		})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "app-tools", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, manifestFile), b, 0o644); err != nil {
			t.Fatal(err)
		}
		built := filepath.Join(programs, "linux-amd64")
		if err := os.MkdirAll(built, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(built, path.Base(program)), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root, programs
}

// Two tools naming the same program file name for one platform are refused: the programs folder
// holds one file by that name, so one tool would be packed with the other's program.
func TestPackAppToolsRefusesTwoToolsWithOneProgramName(t *testing.T) {
	root, programs := appToolsTree(t, map[string]string{"a": "bin/tool", "b": "other/tool", "c": "bin/c"})
	err := packAppTools(root, t.TempDir(), programs, []string{"linux/amd64"}, "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pluxy/a and pluxy/b both name the program file tool for linux/amd64") {
		t.Fatalf("two tools with the program tool: %v", err)
	}
	// Positive control: the same tools without the clash pack.
	root, programs = appToolsTree(t, map[string]string{"a": "bin/tool", "b": "bin/b", "c": "bin/c"})
	if err := packAppTools(root, t.TempDir(), programs, []string{"linux/amd64"}, "", io.Discard); err != nil {
		t.Fatalf("three tools with their own programs: %v", err)
	}
}

// A program that is a link in the programs folder is refused, not followed; and -extensions
// naming no app tool says app tool.
func TestPackAppToolsRefusesALinkedProgramAndNamesAppTools(t *testing.T) {
	root, programs := appToolsTree(t, map[string]string{"a": "bin/a"})
	built := filepath.Join(programs, "linux-amd64", "a")
	elsewhere := filepath.Join(t.TempDir(), "a")
	if err := os.Rename(built, elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, built); err != nil {
		t.Skipf("this account cannot make a symbolic link: %v", err)
	}
	err := packAppTools(root, t.TempDir(), programs, []string{"linux/amd64"}, "", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "is not a regular file (a link or other)") {
		t.Fatalf("a linked program: %v", err)
	}
	err = packAppTools(root, t.TempDir(), programs, []string{"linux/amd64"}, "nosuch", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-extensions: no app tool is named nosuch; the app tools are: a (pluxy/a)") {
		t.Fatalf("-extensions nosuch: %v", err)
	}
}
