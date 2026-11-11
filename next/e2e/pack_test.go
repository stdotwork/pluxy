package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
