package extconfig

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

// The settings folder, a publisher's folder and a settings file each let in only this user and
// SYSTEM: the settings folder by a protected access list, the publisher's folder by the entries
// it inherits from it, and the file by a protected list of its own, after the first save and
// after another. A settings folder that already exists, open to Everyone, is made private when
// settings are loaded, with no save, and a file already in it then holds only the entries it
// inherits from it.
func TestStoreWindowsAccessList(t *testing.T) {
	s := mustParse(t, schemaText)
	dir := filepath.Join(t.TempDir(), "packages", ".settings")
	st := NewStore(dir)
	if _, err := st.Change("pub/ext", s, values(t, `{"name": "n", "token": "t"}`)); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "pub", "ext.json")
	pt.CheckPrivate(t, "new settings folder", dir, true)
	pt.CheckInherited(t, "new publisher's folder", filepath.Join(dir, "pub"), true)
	pt.CheckPrivate(t, "new settings file", file, false)
	if _, err := st.Change("pub/ext", s, values(t, `{"interval": 3}`)); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "settings file after a save", file, false)

	existing := filepath.Join(t.TempDir(), ".settings")
	pt.OpenFolder(t, existing)
	if err := os.Mkdir(filepath.Join(existing, "pub"), 0o700); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(existing, "pub", "ext.json")
	pt.WriteOpenFile(t, left, []byte(`{"values": {"name": "n"}}`))
	st = NewStore(existing)
	got, err := st.Load("pub/ext")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Values["name"]) != `"n"` {
		t.Errorf("loaded %s, want the name n", got)
	}
	pt.CheckPrivate(t, "existing settings folder after loading", existing, true)
	pt.CheckInherited(t, "existing publisher's folder after loading", filepath.Join(existing, "pub"), true)
	pt.CheckInherited(t, "file already in the folder after loading", left, false)
}

// A settings folder another account owns is refused, by loading and by saving, though this user
// may change its list: the owner could open it again. The refusal names the account and says
// how to fix it, and nothing is written.
func TestStoreWindowsRefusesAnotherOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".settings")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pt.SetOwner(t, dir, pt.LocalService)
	st := NewStore(dir)
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), `is owned by NT AUTHORITY\LOCAL SERVICE (S-1-5-19), not by this user`) ||
			!strings.Contains(err.Error(), "takeown /f") || !strings.Contains(err.Error(), "set the extensions' settings again") {
			t.Errorf("%s: error %v, want one naming the owner and how to take the folder over", what, err)
		}
	}
	_, err := st.Load("pub/ext")
	refused("loading", err)
	_, err = st.Change("pub/ext", mustParse(t, schemaText), values(t, `{"name": "n"}`))
	refused("saving", err)
	if names := pt.Names(t, dir); len(names) != 0 {
		t.Errorf("the refused folder holds %v, want nothing", names)
	}
}

// A settings folder that is a link (here a junction) to another folder is refused, not made
// private through the link, and nothing is written through it.
func TestStoreWindowsRefusesALink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, ".settings")
	pt.Junction(t, link, target)
	_, _, before := pt.AccessList(t, target)
	st := NewStore(link)
	if _, err := st.Change("pub/ext", mustParse(t, schemaText), values(t, `{"name": "n"}`)); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Errorf("saving through a link: error %v, want one saying it is a link", err)
	}
	if names := pt.Names(t, target); len(names) != 0 {
		t.Errorf("the link's target holds %v, want nothing", names)
	}
	if _, _, after := pt.AccessList(t, target); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Errorf("the link's target's access list changed:\n%s\nwas\n%s", strings.Join(after, "\n"), strings.Join(before, "\n"))
	}
}

// A settings file another account owns (put there before the folder was private) is skipped
// with a notice naming it and its owner, not loaded; the next save replaces it with core's own.
func TestStoreWindowsSkipsAFileAnotherAccountOwns(t *testing.T) {
	s := mustParse(t, schemaText)
	dir := filepath.Join(t.TempDir(), ".settings")
	st := NewStore(dir)
	if _, err := st.Change("pub/ext", s, values(t, `{"name": "mine"}`)); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "pub", "ext.json")
	if err := os.WriteFile(planted, []byte(`{"values": {"name": "planted"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pt.SetOwner(t, planted, pt.LocalService)
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)
	got, err := st.Load("pub/ext")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Values) != 0 {
		t.Errorf("loaded %s from the file another account owns, want nothing", got)
	}
	if want := "skipped " + planted + `: it is owned by NT AUTHORITY\LOCAL SERVICE (S-1-5-19)`; !strings.Contains(logged.String(), want) {
		t.Errorf("log:\n%s\nwant a line with %q", logged.String(), want)
	}
	if _, err := st.Change("pub/ext", s, values(t, `{"name": "n"}`)); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "file after the save that replaced the planted one", planted, false)
}
