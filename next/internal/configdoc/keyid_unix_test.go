//go:build !windows

package configdoc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKeyIDMakesExistingStateOwnerOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key-id.key")
	if err := os.WriteFile(path, make([]byte, KeyIDSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyID(path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want os.FileMode
	}{
		{"state folder", dir, 0o700},
		{"key file", path, 0o600},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s mode = %o, want %o", tc.name, got, tc.want)
		}
	}
}
