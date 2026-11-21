package buildinfo

import "testing"

// A build with its commit and build date prints them as it always has; a build that recorded
// neither says so in words, and never prints "unknown" as if it were a commit or a date.
func TestFormat(t *testing.T) {
	const (
		commit = "0123456789abcdef0123456789abcdef01234567"
		date   = "2026-11-10T08:00:00Z"
	)
	for _, c := range []struct {
		version, commit, date string
		want                  string
	}{
		{"1.2.3", commit, date, "pluxy 1.2.3 (0123456789abcdef0123456789abcdef01234567, 2026-11-10T08:00:00Z)"},
		{"dev", commit, date, "pluxy dev (0123456789abcdef0123456789abcdef01234567, 2026-11-10T08:00:00Z)"},
		{"dev", "unknown", "unknown", "pluxy dev (no commit or build date recorded)"},
		{"1.2.3", "unknown", "unknown", "pluxy 1.2.3 (no commit or build date recorded)"},
		{"1.2.3", "", "", "pluxy 1.2.3 (no commit or build date recorded)"},
		{"1.2.3", commit, "unknown", "pluxy 1.2.3 (0123456789abcdef0123456789abcdef01234567)"},
		{"1.2.3", "unknown", date, "pluxy 1.2.3 (built 2026-11-10T08:00:00Z)"},
	} {
		if got := Format(c.version, c.commit, c.date); got != c.want {
			t.Errorf("Format(%q, %q, %q) = %q, want %q", c.version, c.commit, c.date, got, c.want)
		}
	}
}
