//go:build !windows

package core

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFirstConfigRefusesAFolderOthersCanChange(t *testing.T) {
	folder := func(t *testing.T, mode fs.FileMode) string {
		dir := filepath.Join(t.TempDir(), "home")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	link := func(t *testing.T) string {
		target := folder(t, 0o700)
		dir := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, dir); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	writable := func(dir, mode string) string {
		return "not writing a config in " + dir + ": other users can add or replace files in it (mode " + mode +
			"), and the config holds your keys. Run chmod go-w " + dir + " and try again, or use another folder."
	}
	for _, c := range []struct {
		name string
		dir  func(t *testing.T) string
		want func(dir string) string
	}{
		{"0777", func(t *testing.T) string { return folder(t, 0o777) }, func(d string) string { return writable(d, "0777") }},
		{"0770", func(t *testing.T) string { return folder(t, 0o770) }, func(d string) string { return writable(d, "0770") }},
		{"0702", func(t *testing.T) string { return folder(t, 0o702) }, func(d string) string { return writable(d, "0702") }},
		{"a link to a 0700 folder", link, func(d string) string {
			return "not writing a config in " + d + ": it is a link. Point PLUXY_HOME or -config at the folder itself."
		}},
		{"0700", func(t *testing.T) string { return folder(t, 0o700) }, nil},
		{"0755", func(t *testing.T) string { return folder(t, 0o755) }, nil},
		{"a 0700 folder under a 0777 one", func(t *testing.T) string {
			return filepath.Join(folder(t, 0o777), "home")
		}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := c.dir(t)
			before, _ := os.Lstat(dir)
			path := filepath.Join(dir, "config.json")
			_, err := WriteFirstConfig(path, 0)
			if c.want == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("no config written: %v", err)
				}
				if before == nil {
					if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
						t.Errorf("the folder made: %v %v, want 0700", st, err)
					}
				}
				return
			}
			if err == nil || err.Error() != c.want(dir) {
				t.Fatalf("error\n%v\nwant\n%s", err, c.want(dir))
			}
			if errors.Is(err, fs.ErrExist) {
				t.Error("the refusal reads as a config already there")
			}
			if names, _ := os.ReadDir(dir); len(names) != 0 {
				t.Errorf("the refused folder holds %d entries", len(names))
			}
			if after, _ := os.Lstat(dir); after.Mode() != before.Mode() {
				t.Errorf("the refused folder's mode changed: %v, was %v", after.Mode(), before.Mode())
			}
		})
	}
}

func TestWriteFirstConfigNeverWritesThroughALink(t *testing.T) {
	for _, c := range []struct {
		name   string
		target bool
		make   func(target, path string) error
	}{
		{"a link to a file", true, os.Symlink},
		{"a link to a missing file", false, os.Symlink},
		{"a hard link to a file", true, os.Link},
	} {
		t.Run(c.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "theirs.json")
			if c.target {
				if err := os.WriteFile(target, []byte("theirs\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			dir := filepath.Join(t.TempDir(), "home")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.json")
			if err := c.make(target, path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path)
			if _, err := WriteFirstConfig(path, 0); !errors.Is(err, fs.ErrExist) {
				t.Errorf("error %v, want a config already there", err)
			}
			if after, err := os.Lstat(path); err != nil || after.Mode() != before.Mode() || !os.SameFile(after, before) {
				t.Errorf("the config path was replaced: %v %v", after, err)
			}
			b, err := os.ReadFile(target)
			switch {
			case c.target && (err != nil || string(b) != "theirs\n"):
				t.Errorf("the linked file now holds %q (%v)", b, err)
			case !c.target && !errors.Is(err, fs.ErrNotExist):
				t.Errorf("the missing file the link names was written: %q %v", b, err)
			}
		})
	}
}
