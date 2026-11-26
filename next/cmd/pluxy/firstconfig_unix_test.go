//go:build !windows

package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// pluxy init into a home folder others can write in exits 1 with the refusal and what to run,
// writes nothing, and on a terminal shows the folder's foreign bytes escaped; a first start of
// pluxy serve gives the same line, not wrapped in another.
func TestInitRefusesAFolderOthersCanChange(t *testing.T) {
	t.Setenv("PLUXY_INIT_PORT", "0")
	h := newTestHome(t)
	dir := filepath.Join(t.TempDir(), "my home \x1b[31m")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	h.env["PLUXY_HOME"] = dir
	line := func(shown, arg string) string {
		return "pluxy: not writing a config in " + shown + ": other users can add or replace files in it (mode 0777), " +
			"and the config holds your keys. Run chmod go-w " + arg + " and try again, or use another folder.\n"
	}
	for _, terminal := range []bool{false, true} {
		old := stderrIsTerminal
		stderrIsTerminal = func(io.Writer) bool { return terminal }
		var out, errOut bytes.Buffer
		code := initConfig([]string{"-port", "0"}, &out, &errOut, h.getenv)
		stderrIsTerminal = old
		want := line(dir, "'"+dir+"'")
		if terminal {
			shown := escapeTableCell(dir)
			want = line(shown, "'"+shown+"'")
		}
		if code != exitFailed || out.Len() != 0 || errOut.String() != want {
			t.Errorf("init (terminal %v): %d %q\n%q\nwant\n%q", terminal, code, out.String(), errOut.String(), want)
		}
	}
	err := firstStart(filepath.Join(dir, "config.json"), true, false)
	if want := line(dir, "'"+dir+"'"); err == nil || "pluxy: "+err.Error()+"\n" != want {
		t.Errorf("first start: %v\nwant\n%s", err, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a config was written: %v", err)
	}
}
