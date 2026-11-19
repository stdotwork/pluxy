package cred

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
)

func recordFor(id string) Record {
	return Record{ID: id, Extension: provID, State: StateActive, Account: route.Account{Models: []route.ModelEntry{{Name: "m"}}},
		Credential: protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}
}

// writeRecordFile writes r to path as the file backend would, but not through it.
func writeRecordFile(t *testing.T, path string, r Record) {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A folder renamed into the credentials folder's place between its check and its opening is
// refused, by loading and by saving: nothing is read from it or written to it. With nothing
// swapped, loading and saving go through the root as before.
func TestFileBackendRefusesASwappedFolder(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(recordFor("acct")); err != nil {
		t.Fatal(err)
	}
	planted, old := filepath.Join(base, "planted"), filepath.Join(base, "old")
	if err := os.Mkdir(planted, 0o700); err != nil {
		t.Fatal(err)
	}
	writeRecordFile(t, filepath.Join(planted, "planted.json"), recordFor("planted"))
	rename := func(from, to string) {
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { beforeRoot = func(string) {} })
	beforeRoot = func(string) {
		rename(dir, old)
		rename(planted, dir)
	}
	back := func() {
		rename(dir, planted)
		rename(old, dir)
	}
	if _, err := Open(FileBackend{Dir: dir}); err == nil || !strings.Contains(err.Error(), "was replaced while it was being opened") {
		t.Errorf("loading from a swapped folder: error %v, want one saying it was replaced", err)
	}
	back()
	if err := (FileBackend{Dir: dir}).Save(recordFor("other")); err == nil || !strings.Contains(err.Error(), "was replaced while it was being opened") {
		t.Errorf("saving into a swapped folder: error %v, want one saying it was replaced", err)
	}
	back()
	if got := names(t, planted); !slices.Equal(got, []string{"planted.json"}) {
		t.Errorf("the swapped-in folder holds %v, want only planted.json", got)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"acct.json"}) {
		t.Errorf("the credentials folder holds %v, want only acct.json", got)
	}

	beforeRoot = func(string) {}
	again, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Get("acct"); !ok {
		t.Error("acct was not loaded")
	}
	if _, err := again.Create(recordFor("other")); err != nil {
		t.Fatal(err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"acct.json", "other.json"}) {
		t.Errorf("after a save the folder holds %v, want acct.json and other.json", got)
	}
}

// A file where the credentials folder should be is refused, by loading and by saving, with a
// credential store error.
func TestFileBackendRefusesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := "credential store: " + path + " is not a folder"
	if _, err := Open(FileBackend{Dir: path}); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("loading: error %v, want %q", err, want)
	}
	if err := (FileBackend{Dir: path}).Save(recordFor("acct")); err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("saving: error %v, want %q", err, want)
	}
}

// A record file that is a link is skipped with a notice naming it, not followed; the other
// records load. Making a link on Windows takes a privilege an elevated administrator holds (CI's
// hosted runners run elevated); without it the test is skipped there, except on CI.
func TestFileBackendSkipsALinkedRecord(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(recordFor("acct")); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(base, "linked.json")
	writeRecordFile(t, elsewhere, recordFor("linked"))
	link := filepath.Join(dir, "linked.json")
	if err := os.Symlink(elsewhere, link); err != nil {
		if runtime.GOOS == "windows" && os.Getenv("CI") == "" {
			t.Skipf("cannot make a link here: %v", err)
		}
		t.Fatal(err)
	}
	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)
	again, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Get("linked"); ok {
		t.Error("the linked record was loaded")
	}
	if _, ok := again.Get("acct"); !ok {
		t.Error("the store's own record was not loaded")
	}
	if want := "skipped " + link + ": it is not a regular file"; !strings.Contains(logged.String(), want) {
		t.Errorf("log:\n%s\nwant a line with %q", logged.String(), want)
	}
}

// CreatePrivate writes a new file only its user can read, never over an existing one, and leaves
// no temporary file behind either way.
func TestCreatePrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := CreatePrivate(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := CreatePrivate(path, []byte("second")); !errors.Is(err, fs.ErrExist) {
		t.Errorf("over an existing file: %v, want fs.ErrExist", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "first" {
		t.Errorf("the file holds %q", b)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the folder holds %d entries, want the file only", len(entries))
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != fileMode {
		t.Errorf("mode %v, want %v", info.Mode().Perm(), fileMode)
	}
}
