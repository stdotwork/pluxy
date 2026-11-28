package server

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

func writeOne(t *testing.T, dir string) []string {
	t.Helper()
	entry := func(status int) *requestEntry {
		return &requestEntry{at: time.Now(), method: "POST", url: &url.URL{Path: "/v1/responses"}, trace: newTrace(),
			body: []byte("client text"), status: status, replyTitle: "RESPONSE"}
	}
	(&RequestLog{Dir: dir, All: true}).write(entry(200))
	(&RequestLog{Dir: dir}).write(entry(500))
	return pt.Names(t, dir)
}

func TestRequestLogWindowsAccessList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	files := writeOne(t, dir)
	if len(files) != 2 {
		t.Fatalf("the logs folder holds %v, want a request log and an error log", files)
	}
	pt.CheckPrivate(t, "new logs folder", dir, true)
	for _, f := range files {
		pt.CheckPrivate(t, f, filepath.Join(dir, f), false)
	}

	existing := filepath.Join(t.TempDir(), "logs")
	pt.OpenFolder(t, existing)
	left := filepath.Join(existing, "main.log")
	pt.WriteOpenFile(t, left, []byte("x"))
	files = writeOne(t, existing)
	if len(files) != 3 {
		t.Fatalf("the logs folder holds %v, want main.log, a request log and an error log", files)
	}
	pt.CheckPrivate(t, "existing logs folder after a request log", existing, true)
	pt.CheckInherited(t, "main.log already in the folder", left, false)
	for _, f := range files {
		if f != "main.log" {
			pt.CheckPrivate(t, f, filepath.Join(existing, f), false)
		}
	}
}

func TestRequestLogWindowsRefusesAFolderAnotherAccountOwns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pt.SetOwner(t, dir, pt.LocalService)
	if files := writeOne(t, dir); len(files) != 0 {
		t.Errorf("the refused folder holds %v, want nothing", files)
	}
}
