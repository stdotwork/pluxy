package cred

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileBackendReportsUnreadableRecords(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	store, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"acct", "other"} {
		if _, err := store.Create(recordFor(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "truncated.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRecordFile(t, filepath.Join(dir, "mismatched.json"), recordFor("elsewhere"))

	var logged bytes.Buffer
	defer log.SetOutput(log.Writer())
	log.SetOutput(&logged)

	again, err := Open(FileBackend{Dir: dir})
	if err != nil {
		t.Fatalf("one unreadable record file fails the open: %v", err)
	}
	if got := len(again.List()); got != 2 {
		t.Fatalf("%d records loaded beside two unreadable files, want 2", got)
	}
	broken := again.Broken()
	if len(broken) != 2 {
		t.Fatalf("broken = %+v, want truncated and mismatched", broken)
	}
	byID := map[string]Broken{}
	for _, b := range broken {
		byID[b.ID] = b
		if want := filepath.Join(dir, b.ID+ext); b.Path != want {
			t.Errorf("%s: path %q, want %q", b.ID, b.Path, want)
		}
		if !strings.HasPrefix(b.Why(), b.Path+": ") {
			t.Errorf("%s: why %q does not start with its path", b.ID, b.Why())
		}
	}
	if b, ok := byID["truncated"]; !ok || !strings.Contains(b.Err.Error(), "unexpected end of JSON input") {
		t.Errorf("truncated: %+v, want the parse error", b)
	}
	if b, ok := byID["mismatched"]; !ok || !strings.Contains(b.Err.Error(), `holds account "elsewhere"`) {
		t.Errorf("mismatched: %+v, want the name mismatch", b)
	}
	for _, want := range []string{"skipped " + filepath.Join(dir, "truncated.json"), "skipped " + filepath.Join(dir, "mismatched.json")} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log:\n%s\nwant a line with %q", logged.String(), want)
		}
	}

	if err := again.Delete("truncated"); err != nil {
		t.Fatalf("deleting the broken account: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "truncated.json")); !os.IsNotExist(err) {
		t.Errorf("after deleting the broken account, its file: %v, want it gone", err)
	}
	if got := again.Broken(); len(got) != 1 || got[0].ID != "mismatched" {
		t.Errorf("broken after the delete = %+v, want only mismatched", got)
	}
	if _, err := again.Create(recordFor("mismatched")); err != nil {
		t.Fatalf("adding an account over a broken file: %v", err)
	}
	if got := again.Broken(); len(got) != 0 {
		t.Errorf("broken after the record was written again = %+v, want none", got)
	}
}
