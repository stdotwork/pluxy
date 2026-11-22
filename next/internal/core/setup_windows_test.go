package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// On Windows, a shell such as Git Bash can set HOME to a different folder than USERPROFILE. The
// scan looks in USERPROFILE, and the CLIProxyAPI file it reports must be the same file importPath
// resolves when the row's ~-prefixed Config path is passed to the import.
func TestSetupScanImportPathWindows(t *testing.T) {
	homeDir := t.TempDir()
	userProfileDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", userProfileDir)
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	want := filepath.Join(userProfileDir, ".cli-proxy-api", "config.yaml")
	writeTestFile(t, want, "port: 8317\n")
	wantInfo, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}

	found := scanMachine(context.Background(), t.TempDir(), "127.0.0.1:8317", false, time.Second)
	if len(found.Imports) != 1 {
		t.Fatalf("scanMachine found %d imports (%+v), want 1 for %s", len(found.Imports), found.Imports, want)
	}
	row := found.Imports[0]
	got, err := importPath(row.Config, "config")
	if err != nil {
		t.Fatalf("importPath(%q) error: %v", row.Config, err)
	}
	gotInfo, err := os.Stat(got)
	if err != nil {
		t.Fatalf("scanMachine reported %q for %s, but importPath resolved it to %s: %v", row.Config, want, got, err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("scanMachine reported %q for %s, but importPath resolved it to a different file %s", row.Config, want, got)
	}
}
