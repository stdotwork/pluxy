package home

import (
	"strings"
	"testing"
)

// Each system's default folder, PLUXY_HOME in its place, and the paths that are refused rather
// than taken from wherever pluxy runs. The expected paths are written out by hand from the
// documented rule (next/README.md, "pluxy's home").
func TestDir(t *testing.T) {
	for _, c := range []struct {
		name, goos string
		env        map[string]string
		want, err  string
	}{
		{name: "linux default", goos: "linux", env: map[string]string{"HOME": "/home/ann"}, want: "/home/ann/.local/share/pluxy"},
		{name: "linux XDG_DATA_HOME", goos: "linux", env: map[string]string{"HOME": "/home/ann", "XDG_DATA_HOME": "/data/ann"}, want: "/data/ann/pluxy"},
		// The XDG Base Directory Specification: a relative path is invalid and is ignored.
		{name: "linux relative XDG_DATA_HOME", goos: "linux", env: map[string]string{"HOME": "/home/ann", "XDG_DATA_HOME": "data"}, want: "/home/ann/.local/share/pluxy"},
		{name: "freebsd default", goos: "freebsd", env: map[string]string{"HOME": "/home/ann"}, want: "/home/ann/.local/share/pluxy"},
		{name: "macOS", goos: "darwin", env: map[string]string{"HOME": "/Users/ann", "XDG_DATA_HOME": "/data/ann"}, want: "/Users/ann/Library/Application Support/pluxy"},
		{name: "windows", goos: "windows", env: map[string]string{"LOCALAPPDATA": "/la", "HOME": "/home/ann"}, want: "/la/pluxy"},
		{name: "PLUXY_HOME", goos: "linux", env: map[string]string{"HOME": "/home/ann", "PLUXY_HOME": "/srv/pluxy/"}, want: "/srv/pluxy"},
		{name: "PLUXY_HOME on macOS", goos: "darwin", env: map[string]string{"PLUXY_HOME": "/srv/pluxy"}, want: "/srv/pluxy"},
		{name: "relative PLUXY_HOME", goos: "linux", env: map[string]string{"HOME": "/home/ann", "PLUXY_HOME": "pluxy"}, err: `PLUXY_HOME="pluxy" is not an absolute path`},
		{name: "no HOME", goos: "linux", env: map[string]string{}, err: "$HOME is not set"},
		{name: "relative HOME", goos: "darwin", env: map[string]string{"HOME": "ann"}, err: "$HOME is not set to an absolute path"},
		{name: "no LOCALAPPDATA", goos: "windows", env: map[string]string{"HOME": "/home/ann"}, err: "LOCALAPPDATA"},
	} {
		got, err := dir(c.goos, func(k string) string { return c.env[k] })
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: %q, %v; want the error %q", c.name, got, err, c.err)
		case c.err != "" && !strings.Contains(err.Error(), "set PLUXY_HOME") && !strings.HasPrefix(err.Error(), "PLUXY_HOME"):
			t.Errorf("%s: %v; want it to say PLUXY_HOME names the folder instead", c.name, err)
		case c.err == "" && (err != nil || got != c.want):
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

// The desktop app's own core is in the app's data folder, which is the data folder pluxy's home
// is in, whatever PLUXY_HOME says.
func TestAppCoreConfig(t *testing.T) {
	for _, c := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"linux", map[string]string{"HOME": "/home/ann", "PLUXY_HOME": "/srv/pluxy"}, "/home/ann/.local/share/dev.pluxy.desktop/core/config.json"},
		{"linux", map[string]string{"HOME": "/home/ann", "XDG_DATA_HOME": "/data/ann"}, "/data/ann/dev.pluxy.desktop/core/config.json"},
		{"darwin", map[string]string{"HOME": "/Users/ann"}, "/Users/ann/Library/Application Support/dev.pluxy.desktop/core/config.json"},
		{"windows", map[string]string{"LOCALAPPDATA": "/la"}, "/la/dev.pluxy.desktop/core/config.json"},
	} {
		if got, err := appCoreConfig(c.goos, func(k string) string { return c.env[k] }); err != nil || got != c.want {
			t.Errorf("%s %v: %q, %v; want %q", c.goos, c.env, got, err, c.want)
		}
	}
}
