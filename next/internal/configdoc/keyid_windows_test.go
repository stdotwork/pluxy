package configdoc

import (
	"os"
	"path/filepath"
	"testing"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

func TestLoadKeyIDWindowsAccessList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "key-id.key")
	if _, err := LoadKeyID(path); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "new state folder", filepath.Dir(path), true)
	pt.CheckPrivate(t, "new key file", path, false)
	if _, err := LoadKeyID(path); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "key file after a later load", path, false)
}

func TestLoadKeyIDWindowsMakesExistingStatePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	pt.OpenFolder(t, dir)
	path := filepath.Join(dir, "key-id.key")
	pt.WriteOpenFile(t, path, make([]byte, KeyIDSize))
	if _, err := LoadKeyID(path); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "existing state folder after loading", dir, true)
	pt.CheckPrivate(t, "existing key file after loading", path, false)
}

func TestLoadKeyIDWindowsRefusesAFolderLink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "key-id.key"), make([]byte, KeyIDSize), 0o600); err != nil {
		t.Fatal(err)
	}
	_, protected, before := pt.AccessList(t, target)
	link := filepath.Join(base, "state")
	pt.Junction(t, link, target)
	if _, err := LoadKeyID(filepath.Join(link, "key-id.key")); err == nil {
		t.Fatal("LoadKeyID accepted a state folder that is a link")
	}
	if _, afterProtected, after := pt.AccessList(t, target); afterProtected != protected || len(after) != len(before) {
		t.Errorf("the link's target access list changed:\n%v\nwas\n%v", after, before)
	}
}
