package logfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

// The logs folder, main.log and a rotated file each have a protected access list that lets in
// only this user and SYSTEM. A logs folder that already exists, open to Everyone, is made private
// when logging to file is turned on, and a main.log already in it then holds only the entries it
// inherits from it.
func TestFilesWindowsAccessList(t *testing.T) {
	l, _ := newFiles(t)
	if err := l.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(l, "pluxy: first\n")
	pt.CheckPrivate(t, "new logs folder", l.Dir(), true)
	pt.CheckPrivate(t, "new main.log", filepath.Join(l.Dir(), Main), false)
	l.mu.Lock()
	l.rotate()
	l.mu.Unlock()
	fmt.Fprint(l, "pluxy: second\n")
	var rotated string
	for _, n := range pt.Names(t, l.Dir()) {
		if n != Main {
			rotated = n
		}
	}
	if rotated == "" {
		t.Fatalf("no rotated file in %v", pt.Names(t, l.Dir()))
	}
	pt.CheckPrivate(t, "rotated file", filepath.Join(l.Dir(), rotated), false)
	pt.CheckPrivate(t, "main.log after a rotation", filepath.Join(l.Dir(), Main), false)
	l.Configure(false, 0)

	existing := New(filepath.Join(t.TempDir(), "logs"))
	pt.OpenFolder(t, existing.Dir())
	left := filepath.Join(existing.Dir(), Main)
	pt.WriteOpenFile(t, left, []byte("[2026-11-09 12:00:00.000] [info ] before\n"))
	if err := existing.Configure(true, 0); err != nil {
		t.Fatal(err)
	}
	defer existing.Configure(false, 0)
	pt.CheckPrivate(t, "existing logs folder after turning logging on", existing.Dir(), true)
	pt.CheckInherited(t, "main.log already in the folder", left, false)
}

// A main.log another account owns (put there before the folder was private) is refused, not
// written to: that account could read it.
func TestFilesWindowsRefusesAMainLogAnotherAccountOwns(t *testing.T) {
	l, _ := newFiles(t)
	if err := os.MkdirAll(l.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(l.Dir(), Main)
	if err := os.WriteFile(planted, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pt.SetOwner(t, planted, pt.LocalService)
	err := l.Configure(true, 0)
	if err == nil || !strings.Contains(err.Error(), `is owned by NT AUTHORITY\LOCAL SERVICE (S-1-5-19), not by this user`) {
		t.Errorf("turning logging on: error %v, want one naming the owner", err)
	}
	defer l.Configure(false, 0)
	fmt.Fprint(l, "pluxy: secret line\n")
	if info, err := os.Stat(planted); err != nil || info.Size() != 0 {
		t.Errorf("main.log another account owns was written: %v %v", info, err)
	}
}

// A logs folder another account owns is refused, as the credentials folder is: logging to file
// stays off with an error naming the owner, and nothing is written.
func TestFilesWindowsRefusesAFolderAnotherAccountOwns(t *testing.T) {
	l, _ := newFiles(t)
	if err := os.MkdirAll(l.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	pt.SetOwner(t, l.Dir(), pt.LocalService)
	err := l.Configure(true, 0)
	if err == nil || !strings.Contains(err.Error(), `is owned by NT AUTHORITY\LOCAL SERVICE (S-1-5-19), not by this user`) ||
		!strings.Contains(err.Error(), "takeown /f") {
		t.Errorf("turning logging on: error %v, want one naming the owner and how to take the folder over", err)
	}
	defer l.Configure(false, 0)
	fmt.Fprint(l, "pluxy: secret line\n")
	if names := pt.Names(t, l.Dir()); len(names) != 0 {
		t.Errorf("the refused folder holds %v, want nothing", names)
	}
}
