package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	pt "github.com/stdotwork/pluxy/next/internal/private/privatetest"
)

func TestWriteFirstConfigWindows(t *testing.T) {
	made := filepath.Join(t.TempDir(), "new")
	path := filepath.Join(made, "config.json")
	if _, err := WriteFirstConfig(path, 0); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "a folder made for the first config", made, true)
	pt.CheckPrivate(t, "the first config", path, false)

	open := filepath.Join(t.TempDir(), "open")
	pt.OpenFolder(t, open)
	path = filepath.Join(open, "config.json")
	if _, err := WriteFirstConfig(path, 0); err != nil {
		t.Fatal(err)
	}
	pt.CheckPrivate(t, "a folder open to Everyone, after the first config", open, true)
	pt.CheckPrivate(t, "the first config in it", path, false)

	target := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	pt.Junction(t, link, target)
	_, err := WriteFirstConfig(filepath.Join(link, "config.json"), 0)
	if err == nil || !strings.HasPrefix(err.Error(), "not writing a config in "+link+": ") || !strings.Contains(err.Error(), "is a link") {
		t.Errorf("a junction: %v, want the link refused", err)
	}
	if names := pt.Names(t, target); !slices.Equal(names, []string(nil)) {
		t.Errorf("the folder the junction points to holds %v", names)
	}
}
