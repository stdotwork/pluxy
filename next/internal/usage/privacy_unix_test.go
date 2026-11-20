//go:build !windows

package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The usage folder, a day file and a summary file are owner-only on unix, including a folder
// that already existed with wider access.
func TestUsageOwnerOnlyModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)}
	s, err := openStore(Options{Dir: dir}, c.now, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(Event{Timestamp: c.now(), Account: "account", KeyID: "key"})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	sumDir := filepath.Join(dir, summarySubdir)
	sumPath := filepath.Join(sumDir, "2026-11-18"+summarySuffix)
	if err := writeSummaryFileAtomic(sumDir, sumPath, &daySummaryFile{V: summaryVersion, Day: "2026-11-18", Rows: []summaryRow{}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		path string
		want os.FileMode
	}{
		{"usage folder", dir, 0o700},
		{"day file", filepath.Join(dir, "2026-11-19"+daySuffix), 0o600},
		{"summary folder", sumDir, 0o700},
		{"summary file", sumPath, 0o600},
	} {
		info, err := os.Stat(tc.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != tc.want {
			t.Errorf("%s mode = %o, want %o", tc.name, got, tc.want)
		}
	}
}
