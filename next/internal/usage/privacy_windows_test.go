package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

// The usage folder, a day file, a summary file and the seq mark each have a protected access list
// that lets in only this user and SYSTEM.
func TestUsageWindowsAccessList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage")
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(Event{Timestamp: c.now(), Account: "account", KeyID: "key"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	day := filepath.Join(dir, "2026-11-19"+daySuffix)
	pt.CheckPrivate(t, "new usage folder", dir, true)
	pt.CheckPrivate(t, "new day file", day, false)
	pt.CheckPrivate(t, "new seq mark", filepath.Join(dir, seqFile), false)

	sumDir := filepath.Join(dir, summarySubdir)
	sumPath := filepath.Join(sumDir, "2026-11-18"+summarySuffix)
	if err := writeSummaryFileAtomic(sumDir, sumPath, &daySummaryFile{V: summaryVersion, Day: "2026-11-18", Rows: []summaryRow{}}); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "new summary folder", sumDir, true)
	pt.CheckPrivate(t, "new summary file", sumPath, false)
}

// A usage folder that already exists, open to Everyone, is made private before its day file is
// written, and the day file is private too.
func TestUsageWindowsMakesExistingFolderPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage")
	pt.OpenFolder(t, dir)
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(Event{Timestamp: c.now(), Account: "account", KeyID: "key"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "existing usage folder after writing", dir, true)
	pt.CheckPrivate(t, "day file in an existing folder", filepath.Join(dir, "2026-11-19"+daySuffix), false)
}

// A usage folder that is a link (here a junction) is refused, not made private through the
// link, and no day file is written through it.
func TestUsageWindowsRefusesALink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "usage")
	pt.Junction(t, link, target)
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	s, err := openStore(Options{Dir: link}, c.now, nil)
	if err == nil {
		s.Close()
		t.Fatal("Open accepted a usage folder that is a link")
	}
	if names := pt.Names(t, target); len(names) != 0 {
		t.Errorf("the link's target holds %v, want nothing", names)
	}
}
