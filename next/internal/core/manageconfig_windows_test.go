package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

func replaceKeepsList(t *testing.T, path string) {
	t.Helper()
	_, protected, before := pt.AccessList(t, path)
	if err := writeFileAtomic(path, []byte("new contents")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "new contents" {
		t.Fatalf("contents = %q, %v", b, err)
	}
	if _, p, after := pt.AccessList(t, path); p != protected || !slices.Equal(after, before) {
		t.Errorf("access list after the write (protected=%v):\n%s\nwas (protected=%v):\n%s", p,
			strings.Join(after, "\n"), protected, strings.Join(before, "\n"))
	}
	if names := dirNames(t, filepath.Dir(path)); len(names) != 1 {
		t.Errorf("folder holds %v, want only %s", names, filepath.Base(path))
	}
}

func TestWriteFileAtomicWindowsKeepsATighterList(t *testing.T) {
	me, _ := pt.ThisUser(t)
	dir := filepath.Join(t.TempDir(), "open")
	pt.OpenFolder(t, dir)
	path := filepath.Join(dir, "config.json")
	pt.WriteOpenFile(t, path, []byte("old"))
	pt.Icacls(t, path, "/inheritance:r", "/grant:r", "*"+me+":F", "*"+pt.System+":F")
	if pt.LetsEveryoneIn(t, path) {
		t.Fatal("setup: the config still lets Everyone in")
	}
	replaceKeepsList(t, path)
	if pt.LetsEveryoneIn(t, path) {
		t.Error("the config lets Everyone in after the write")
	}
}

func TestWriteFileAtomicWindowsKeepsAnInheritingList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	pt.Icacls(t, path, "/grant", "*"+pt.LocalService+":R")
	if _, protected, entries := pt.AccessList(t, path); protected ||
		!slices.ContainsFunc(entries, func(e string) bool { return strings.HasPrefix(e, pt.LocalService+" ") }) {
		t.Fatalf("setup: want an inheriting list with LOCAL SERVICE's entry, protected=%v:\n%s", protected,
			strings.Join(entries, "\n"))
	}
	replaceKeepsList(t, path)
}
