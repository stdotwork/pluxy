package core

import (
	"os"
	"path/filepath"
	"testing"
)

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWriteFileAtomicKeepsPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, []byte("old"), mode); err != nil {
				t.Fatal(err)
			}
			// WriteFile is subject to the umask; set the mode the test means to check.
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if err := writeFileAtomic(path, []byte("new contents")); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(path)
			if err != nil || string(b) != "new contents" {
				t.Fatalf("contents = %q, %v", b, err)
			}
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != mode {
				t.Errorf("mode = %v, want %v", fi.Mode().Perm(), mode)
			}
			if names := dirNames(t, dir); len(names) != 1 || names[0] != "config.json" {
				t.Errorf("folder holds %v, want only config.json", names)
			}
		})
	}
}

func TestWriteFileAtomicWritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir, linkDir := filepath.Join(dir, "real"), filepath.Join(dir, "links")
	for _, d := range []string{realDir, linkDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(realDir, "config.json")
	link := filepath.Join(linkDir, "config.json")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if err := writeFileAtomic(link, []byte("through the link")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file")
	}
	if dest, err := os.Readlink(link); err != nil || dest != target {
		t.Errorf("link points to %q, %v; want %q", dest, err, target)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "through the link" {
		t.Errorf("target holds %q, %v", b, err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o640 {
		t.Errorf("target mode = %v, %v; want 0640", fi.Mode().Perm(), err)
	}
	// No temp file beside the target or beside the link.
	if names := dirNames(t, realDir); len(names) != 1 {
		t.Errorf("target folder holds %v", names)
	}
	if names := dirNames(t, linkDir); len(names) != 1 {
		t.Errorf("link folder holds %v", names)
	}
}

func TestWriteFileAtomicFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileAtomic(filepath.Join(dir, "missing.json"), []byte("x")); err == nil {
		t.Error("writing a file that is not there succeeded")
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Errorf("folder holds %v, want nothing", names)
	}
}

// A write that fails after the temp file exists (here the final rename, because the target is
// a folder) removes the temp file.
func TestWriteFileAtomicRemovesTempFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.json")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("x")); err == nil {
		t.Fatal("replacing a folder with a file succeeded")
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "config.json" {
		t.Errorf("folder holds %v, want only config.json", names)
	}
}
