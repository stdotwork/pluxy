package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeModule writes a module the way next is laid out: one extension under extensions/ and one
// archived extension under archive/, which is a module of its own. Each is a program that does
// nothing, so packing it needs no network. It returns the module's folder.
func fakeModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	platform := runtime.GOOS + "/" + runtime.GOARCH
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifestOf := func(id, program string) string {
		b, err := json.Marshal(map[string]any{
			"manifest_version": 0, "id": id, "version": "0.1.0", "title": "Fake", "summary": "A fake extension.",
			"protocol": []int{0}, "programs": map[string]string{platform: program},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	const program = "package main\n\nfunc main() {}\n"
	write("go.mod", "module example.com/fake\n\ngo 1.24\n")
	write("extensions/current/pluxy-extension.json", manifestOf("fake/current", "current"))
	write("extensions/current/main.go", program)
	write("archive/README.md", "Archived extensions.\n")
	write("archive/old/go.mod", "module example.com/fake/archive/old\n\ngo 1.24\n")
	write("archive/old/pluxy-extension.json", manifestOf("fake/old", "old"))
	write("archive/old/main.go", program)
	return root
}

// packed runs pluxy-pack in module root with args and returns the index it wrote.
func packed(t *testing.T, root string, args ...string) (index, string, error) {
	t.Helper()
	t.Chdir(root)
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run(append([]string{"-out", out}, args...), &stdout, &stderr)
	if err != nil {
		return index{}, out, err
	}
	b, err := os.ReadFile(filepath.Join(out, "bundled", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ix index
	if err := json.Unmarshal(b, &ix); err != nil {
		t.Fatal(err)
	}
	return ix, out, nil
}

// Archived extensions are packed only when asked: without -archived they are neither packed nor
// named, and with it each is built in its own module and listed as unverified, while the
// extensions pluxy ships keep their tier.
func TestPackArchivedOnlyWhenAsked(t *testing.T) {
	root := fakeModule(t)

	ix, _, err := packed(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Packages["fake/old"]; ok || len(ix.Packages) != 1 {
		t.Errorf("packed without -archived: %v", ix.Packages)
	}
	if _, _, err := packed(t, root, "-extensions", "old"); err == nil || !strings.Contains(err.Error(), "no extension is named old") {
		t.Errorf("-extensions old without -archived: %v", err)
	}

	ix, out, err := packed(t, root, "-archived")
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Packages) != 2 || ix.Packages["fake/current"].Tier != "verified" || ix.Packages["fake/old"].Tier != "unverified" {
		t.Errorf("packed with -archived: %+v", ix.Packages)
	}
	old := ix.Packages["fake/old"].Releases[0].Artifacts[runtime.GOOS+"/"+runtime.GOARCH]
	if _, err := os.Stat(filepath.Join(out, "bundled", old.URL)); err != nil || old.Size == 0 || old.SHA256 == "" {
		t.Errorf("the archived extension's archive: %+v, %v", old, err)
	}

	ix, _, err = packed(t, root, "-archived", "-extensions", "fake/old")
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Packages) != 1 || ix.Packages["fake/old"].Tier != "unverified" {
		t.Errorf("packed only the archived extension: %+v", ix.Packages)
	}
}
