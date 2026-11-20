package configdoc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadKeyIDPersistsAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "key-id.key")

	first, err := LoadKeyID(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}

	second, err := LoadKeyID(path)
	if err != nil {
		t.Fatal(err)
	}
	if id1, id2 := first.ID("sk-client-1"), second.ID("sk-client-1"); id1 != id2 || id1 == "" {
		t.Errorf("IDs across reloads = %q vs %q, want identical non-empty IDs", id1, id2)
	}
}

func TestLoadKeyIDRefusesCorruptOrSymlink(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.key")
	if err := os.WriteFile(bad, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeyID(bad); err == nil {
		t.Error("LoadKeyID accepted short key file")
	}

	if runtime.GOOS != "windows" {
		target := filepath.Join(dir, "real.key")
		if err := os.WriteFile(target, make([]byte, KeyIDSize), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "link.key")
		if err := os.Symlink(target, link); err == nil {
			if _, err := LoadKeyID(link); err == nil {
				t.Error("LoadKeyID accepted symlink")
			}
		}
	}
}
