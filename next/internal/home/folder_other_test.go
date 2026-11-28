//go:build !windows

package home

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

func TestOpenDirRefusesAnotherUsersFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := "user ID " + strconv.Itoa(os.Geteuid())
	if u, err := user.LookupId(strconv.Itoa(os.Geteuid())); err == nil && u.Username != "" {
		owner = u.Username
	}
	euid = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { euid = os.Geteuid })
	root, err := OpenDir(dir)
	if err == nil {
		root.Close()
		t.Fatal("another user's folder was opened")
	}
	want := "not writing a config in " + dir + ": it belongs to " + owner +
		", not to you, so they could change your keys. Use a folder of your own (PLUXY_HOME or -config)."
	var refused *Refusal
	if !errors.As(err, &refused) || err.Error() != want {
		t.Errorf("error\n%v\nwant\n%s", err, want)
	}

	euid = os.Geteuid
	root, err = OpenDir(dir)
	if err != nil {
		t.Fatalf("this user's own folder: %v", err)
	}
	root.Close()
}

func TestShellArg(t *testing.T) {
	for in, want := range map[string]string{
		"/home/ann/.local/share/pluxy":           "/home/ann/.local/share/pluxy",
		"/Users/ann/Library/Application Support": "'/Users/ann/Library/Application Support'",
		"/tmp/it's $HOME":                        `'/tmp/it'\''s $HOME'`,
	} {
		if got := shellArg(in); got != want {
			t.Errorf("shellArg(%q) = %s, want %s", in, got, want)
		}
	}
}
