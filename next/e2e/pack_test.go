package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// packManifest is the part of an extension's manifest the pack test reads.
type packManifest struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Protocol []int  `json:"protocol"`
}

// packIndex is a registry index as the pack tool writes it.
type packIndex struct {
	RegistryVersion int `json:"registry_version"`
	Packages        map[string]struct {
		Releases []struct {
			Version   string `json:"version"`
			Protocol  []int  `json:"protocol"`
			Artifacts map[string]struct {
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"artifacts"`
		} `json:"releases"`
	} `json:"packages"`
}

// extensionManifests returns the manifests of every extension folder, by package ID.
func extensionManifests(t *testing.T) map[string]packManifest {
	t.Helper()
	dir := filepath.Join("..", "extensions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[string]packManifest{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "pluxy-extension.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var m packManifest
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		if m.ID == "" {
			t.Fatalf("%s: manifest has no id", e.Name())
		}
		manifests[m.ID] = m
	}
	if len(manifests) == 0 {
		t.Fatal("no extension manifests under ../extensions")
	}
	return manifests
}

// buildPackTool builds the pack tool from this module and returns its path.
func buildPackTool(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pluxy-pack")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/pluxy-pack")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building pluxy-pack: %v: %s", err, out)
	}
	return bin
}

// runPack packs the extensions with the pack tool into out and returns its output.
func runPack(t *testing.T, bin, out string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"-out", out}, args...)...)
	cmd.Dir = ".."
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pluxy-pack %v: %v: %s", args, err, b)
	}
	return string(b)
}

// readPackIndex reads the bundled index of a packed folder.
func readPackIndex(t *testing.T, out string) packIndex {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "bundled", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ix packIndex
	if err := json.Unmarshal(b, &ix); err != nil {
		t.Fatal(err)
	}
	return ix
}

// packCore starts core with an empty packages folder and the bundled index at index, and
// returns core's base URL.
func packCore(t *testing.T, index string) string {
	t.Helper()
	dir := t.TempDir()
	cfg := map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(dir, "credentials"),
		"packages":        map[string]any{"dir": filepath.Join(dir, "packages")},
	}
	base, _, _ := runCoreArgs(t, cfg, "-bundled", index)
	return base
}

// The pack tool writes an archive for every extension and platform it is asked for, and a
// bundled index that pins each archive; a fresh core installs and enables every one.
func TestPackToolBundlesEveryExtension(t *testing.T) {
	t.Parallel()
	manifests := extensionManifests(t)
	bin := buildPackTool(t)
	out := t.TempDir()
	runPack(t, bin, out)

	ix := readPackIndex(t, out)
	if ix.RegistryVersion != 0 {
		t.Fatalf("registry_version = %d, want 0", ix.RegistryVersion)
	}
	if len(ix.Packages) != len(manifests) {
		t.Fatalf("index lists %d packages, want the %d extensions", len(ix.Packages), len(manifests))
	}
	digests := map[string]string{}
	for id, m := range manifests {
		p, ok := ix.Packages[id]
		if !ok {
			t.Errorf("index does not list %s", id)
			continue
		}
		if len(p.Releases) != 1 {
			t.Errorf("%s: %d releases, want 1", id, len(p.Releases))
			continue
		}
		rel := p.Releases[0]
		if rel.Version != m.Version {
			t.Errorf("%s: release version %q, want the manifest's %q", id, rel.Version, m.Version)
		}
		if !slices.Equal(rel.Protocol, m.Protocol) {
			t.Errorf("%s: release protocol %v, want the manifest's %v", id, rel.Protocol, m.Protocol)
		}
		art, ok := rel.Artifacts[platform]
		if !ok {
			t.Errorf("%s: no archive for %s", id, platform)
			continue
		}
		b, err := os.ReadFile(filepath.Join(out, "bundled", filepath.FromSlash(art.URL)))
		if err != nil {
			t.Errorf("%s: %v", id, err)
			continue
		}
		sum := sha256.Sum256(b)
		digest := hex.EncodeToString(sum[:])
		if art.SHA256 != digest {
			t.Errorf("%s: index pins sha256 %s, the archive's is %s", id, art.SHA256, digest)
		}
		if art.Size != int64(len(b)) {
			t.Errorf("%s: index pins size %d, the archive's is %d", id, art.Size, len(b))
		}
		digests[id] = digest
	}

	base := packCore(t, filepath.Join(out, "bundled", "index.json"))
	installed := map[string]packageView{}
	for _, p := range listPackages(t, base) {
		installed[p.ID] = p
	}
	if len(installed) != len(digests) {
		t.Errorf("core lists %d packages, want %d", len(installed), len(digests))
	}
	for id, digest := range digests {
		p, ok := installed[id]
		if !ok {
			t.Errorf("core does not list %s", id)
			continue
		}
		if !p.Enabled {
			t.Errorf("%s is not enabled: %+v", id, p)
		}
		if p.SHA256 != digest {
			t.Errorf("%s: core records sha256 %s, the archive's is %s", id, p.SHA256, digest)
		}
	}
}

// Packing the same extensions twice gives byte-identical archives and index.
func TestPackToolIsReproducible(t *testing.T) {
	t.Parallel()
	bin := buildPackTool(t)
	first, second := t.TempDir(), t.TempDir()
	runPack(t, bin, first)
	runPack(t, bin, second)

	a := packTree(t, filepath.Join(first, "bundled"))
	b := packTree(t, filepath.Join(second, "bundled"))
	if len(a) == 0 {
		t.Fatal("the first pack wrote no files")
	}
	for name, data := range a {
		other, ok := b[name]
		if !ok {
			t.Errorf("%s is missing from the second pack", name)
			continue
		}
		if !bytes.Equal(data, other) {
			t.Errorf("%s differs between two packs", name)
		}
	}
	for name := range b {
		if _, ok := a[name]; !ok {
			t.Errorf("%s is extra in the second pack", name)
		}
	}
}

// packTree reads every file under dir, by its slash-separated relative name.
func packTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)], err = os.ReadFile(p)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// appToolManifest is the part of an app tool's manifest the pack test reads. An app tool's pages
// are under app.ui, as the app reads them.
type appToolManifest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	App     struct {
		Programs map[string]string `json:"programs"`
		UI       *struct {
			Dir string `json:"dir"`
		} `json:"ui"`
	} `json:"app"`
}

// appToolManifests returns the manifests of every app-tools folder but the test fixture, by
// package ID.
func appToolManifests(t *testing.T) map[string]appToolManifest {
	t.Helper()
	dir := filepath.Join("..", "app-tools")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	manifests := map[string]appToolManifest{}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "usage-fixture" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "pluxy-extension.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var m appToolManifest
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		manifests[m.ID] = m
	}
	if len(manifests) < 10 {
		t.Fatalf("only %d app tool manifests under ../app-tools", len(manifests))
	}
	withUI := 0
	for _, m := range manifests {
		if m.App.UI != nil {
			withUI++
		}
	}
	if withUI == 0 {
		t.Fatal("no app tool manifest has app.ui: the pages check would check nothing")
	}
	return manifests
}

// packedFile is one file of a release archive: its mode and bytes.
type packedFile struct {
	mode int64
	body []byte
}

// archiveFiles reads a release archive: each regular file's mode and bytes, by name.
func archiveFiles(t *testing.T, b []byte) map[string]packedFile {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	files := map[string]packedFile{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = packedFile{h.Mode, body}
	}
}

// The pack tool packs every app tool (but the test fixture) for each platform its manifest
// names among those asked, from the programs built beforehand: each archive holds the manifest's
// own bytes, the program at the path the manifest names and the files of its app.ui folder, and
// an index of the app's registry form pins each archive. A platform a manifest does not name is
// left out, a missing program fails, and packing twice gives the same bytes.
func TestPackToolPacksAppTools(t *testing.T) {
	t.Parallel()
	manifests := appToolManifests(t)
	platforms := []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64"}
	programs := t.TempDir()
	want := 0
	for id, m := range manifests {
		for _, p := range platforms {
			program, ok := m.App.Programs[p]
			if !ok {
				continue
			}
			want++
			dir := filepath.Join(programs, strings.ReplaceAll(p, "/", "-"))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := []byte(id + " for " + p)
			if err := os.WriteFile(filepath.Join(dir, path.Base(program)), body, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	bin := buildPackTool(t)
	out := t.TempDir()
	runPack(t, bin, out, "-app-tools", programs, "-platforms", strings.Join(platforms, ","))
	again := t.TempDir()
	runPack(t, bin, again, "-app-tools", programs, "-platforms", strings.Join(platforms, ","))
	if a, b := packTree(t, filepath.Join(out, "app-tools")), packTree(t, filepath.Join(again, "app-tools")); !maps.EqualFunc(a, b, bytes.Equal) {
		t.Error("two packs of the app tools differ")
	}

	dest := filepath.Join(out, "app-tools")
	b, err := os.ReadFile(filepath.Join(dest, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ix packIndex
	if err := json.Unmarshal(b, &ix); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(`"protocol"`)) {
		t.Errorf("the app tools' index names a protocol: %s", b)
	}
	if _, ok := ix.Packages["pluxy/usage-fixture"]; ok {
		t.Error("the index lists the test fixture pluxy/usage-fixture")
	}
	archives, _ := filepath.Glob(filepath.Join(dest, "*.tar.gz"))
	if len(archives) != want {
		t.Errorf("%d archives, want %d (each tool's named platforms)", len(archives), want)
	}
	for id, m := range manifests {
		p, ok := ix.Packages[id]
		if !ok || len(p.Releases) != 1 {
			t.Errorf("index lists %s as %+v, want one release", id, p)
			continue
		}
		rel := p.Releases[0]
		if rel.Version != m.Version {
			t.Errorf("%s: release version %q, want %q", id, rel.Version, m.Version)
		}
		raw, err := os.ReadFile(filepath.Join("..", "app-tools", filepath.Base(manifestFolder(t, id)), "pluxy-extension.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, platform := range platforms {
			program, named := m.App.Programs[platform]
			art, packed := rel.Artifacts[platform]
			if named != packed {
				t.Errorf("%s: %s named %v, packed %v", id, platform, named, packed)
				continue
			}
			if !packed {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dest, art.URL))
			if err != nil {
				t.Errorf("%s: %v", id, err)
				continue
			}
			sum := sha256.Sum256(b)
			if art.SHA256 != hex.EncodeToString(sum[:]) || art.Size != int64(len(b)) {
				t.Errorf("%s %s: the index pins %s/%d, the archive is %x/%d", id, platform, art.SHA256, art.Size, sum, len(b))
			}
			files := archiveFiles(t, b)
			if f := files[program]; f.mode != 0o755 || string(f.body) != id+" for "+platform {
				t.Errorf("%s %s: %s holds %q mode %o", id, platform, program, f.body, f.mode)
			}
			if !bytes.Equal(files["pluxy-extension.json"].body, raw) {
				t.Errorf("%s %s: the archive's manifest is not the tool's", id, platform)
			}
			// Beside the manifest and the program, the archive holds exactly the files of the
			// tool's app.ui folder, each with its bytes and its mode (0755 when any execute bit is
			// set, else 0644).
			want := map[string]packedFile{"pluxy-extension.json": {0o644, raw}, program: {0o755, files[program].body}}
			if m.App.UI != nil {
				maps.Copy(want, uiTree(t, filepath.Join("..", "app-tools", manifestFolder(t, id)), m.App.UI.Dir))
			}
			for name, w := range want {
				g, ok := files[name]
				switch {
				case !ok:
					t.Errorf("%s %s: the archive does not hold %s", id, platform, name)
				case g.mode != w.mode || !bytes.Equal(g.body, w.body):
					t.Errorf("%s %s: the archive holds %s with mode %o and %d bytes, want mode %o and the tool's %d bytes", id, platform, name, g.mode, len(g.body), w.mode, len(w.body))
				}
			}
			for name := range files {
				if _, ok := want[name]; !ok {
					t.Errorf("%s %s: the archive holds %s, which is not the tool's", id, platform, name)
				}
			}
		}
	}

	// A program missing for a platform a manifest names: the pack fails and says which.
	if err := os.Remove(filepath.Join(programs, "windows-amd64", "claude-code.exe")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-out", t.TempDir(), "-app-tools", programs, "-platforms", "windows/amd64")
	cmd.Dir = ".."
	got, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(got, []byte("pluxy/claude-code: its program for windows/amd64")) {
		t.Errorf("packing without claude-code.exe: %v: %s", err, got)
	}
}

// uiTree returns every file under dir/ui as the pack tool must pack it: by its slash-separated
// path inside the package (ui/...), with 0755 when any execute bit is set, else 0644.
func uiTree(t *testing.T, dir, ui string) map[string]packedFile {
	t.Helper()
	files := map[string]packedFile{}
	root := filepath.Join(dir, filepath.FromSlash(ui))
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		mode := int64(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		files[path.Join(ui, filepath.ToSlash(rel))] = packedFile{mode, body}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("%s holds no files", root)
	}
	return files
}

// manifestFolder returns the app-tools folder whose manifest has package ID id.
func manifestFolder(t *testing.T, id string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "app-tools"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("..", "app-tools", e.Name(), "pluxy-extension.json"))
		if err != nil {
			continue
		}
		var m appToolManifest
		if json.Unmarshal(b, &m) == nil && m.ID == id {
			return e.Name()
		}
	}
	t.Fatalf("no app-tools folder has %s", id)
	return ""
}
