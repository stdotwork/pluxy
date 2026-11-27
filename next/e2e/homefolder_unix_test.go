//go:build !windows

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A first pluxy serve or pluxy init into a home folder that other users can write in, or that
// is a link, exits 1 with the refusal and what to do, and writes nothing there.
func TestPluxysHomeOthersCanChangeIsRefused(t *testing.T) {
	h := newPluxyHome(t)
	open := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(open, 0o770); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, want string }{
		{open, "pluxy: not writing a config in " + open + ": other users can add or replace files in it (mode 0770), " +
			"and the config holds your keys. Run chmod go-w " + open + " and try again, or use another folder.\n"},
		{link, "pluxy: not writing a config in " + link + ": it is a link. Point PLUXY_HOME or -config at the folder itself.\n"},
	} {
		for _, args := range [][]string{{"serve", "-watch-interval", "0"}, {"init"}} {
			r := h.run(t, t.TempDir(), []string{"PLUXY_HOME=" + c.dir}, args...)
			if r.code != 1 || !strings.HasSuffix(r.stderr, c.want) || r.stdout != "" {
				t.Errorf("pluxy %s with PLUXY_HOME=%s: %s\nwant exit 1 and stderr ending\n%s", args[0], c.dir, r, c.want)
			}
			if names, _ := os.ReadDir(c.dir); len(names) != 0 {
				t.Errorf("pluxy %s wrote %d entries in %s", args[0], len(names), c.dir)
			}
		}
	}
}
