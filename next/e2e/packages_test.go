package e2e

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
)

const platform = runtime.GOOS + "/" + runtime.GOARCH

// codexArchive packs the built Codex extension as a release archive of version and returns
// the archive.
func codexArchive(t *testing.T, version string) []byte {
	t.Helper()
	prog, err := os.ReadFile(filepath.Join(binDir, "codex", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(map[string]any{
		"manifest_version": 0, "id": "pluxy/codex", "version": version, "protocol": []int{0},
		"programs": map[string]string{platform: "bin/codex"},
	})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		mode int64
		body []byte
	}{{"pluxy-extension.json", 0o644, m}, {"bin/", 0o755, nil}, {"bin/codex", 0o755, prog}} {
		h := &tar.Header{Name: f.name, Mode: f.mode, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(f.name, "/") {
			h.Typeflag = tar.TypeDir
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeRegistry writes a local registry offering archive as pluxy/codex 0.1.0 into a new
// folder and returns the index's path. digest is the SHA-256 it lists; empty lists the
// archive's own.
func writeRegistry(t *testing.T, archive []byte, digest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex.tar.gz"), archive, 0o644); err != nil {
		t.Fatal(err)
	}
	if digest == "" {
		sum := sha256.Sum256(archive)
		digest = hex.EncodeToString(sum[:])
	}
	index, _ := json.Marshal(map[string]any{
		"registry_version": 0,
		"packages": map[string]any{"pluxy/codex": map[string]any{"releases": []any{map[string]any{
			"version": "0.1.0", "protocol": []int{0},
			"artifacts": map[string]any{platform: map[string]any{"url": "codex.tar.gz", "sha256": digest, "size": len(archive)}},
		}}}},
	})
	path := filepath.Join(dir, "index.json")
	if err := os.WriteFile(path, index, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// answeringUpstream is a fake OpenAI-compatible upstream that answers every request at once.
func answeringUpstream(t *testing.T) *upstream {
	u := newUpstream(t)
	u.respond = u.streamReply
	close(u.release)
	return u
}

type packageView struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	Error   string `json:"error"`
	SHA256  string `json:"sha256"`
}

type packageList struct {
	Packages []packageView `json:"packages"`
}

// packagesCore runs core with the openai-compatible extension from the config (serving model
// gpt-4o from u), the registry at index, and settings for pluxy/codex that give it one account
// on srv serving gpt-6.1-sol. It returns core's base URL and its packages folder.
func packagesCore(t *testing.T, u *upstream, srv *codextest.Server, index string, args ...string) (string, string) {
	t.Helper()
	pkgDir := filepath.Join(t.TempDir(), "packages")
	base, _, _ := runCoreArgs(t, packagesConfig(u, srv, index, pkgDir), args...)
	return base, pkgDir
}

func packagesConfig(u *upstream, srv *codextest.Server, index, pkgDir string) map[string]any {
	cfg := map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(filepath.Dir(pkgDir), "credentials"),
		"packages":        map[string]any{"dir": pkgDir},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "upstream-1", "models": []string{"gpt-4o"},
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
		"extensions": map[string]any{"pluxy/codex": map[string]any{"accounts": []any{map[string]any{
			"id": "codex-key", "models": []string{"gpt-6.1-sol"},
			"settings":   map[string]any{"base_url": srv.URL},
			"credential": map[string]any{"kind": "api_key", "api_key": srv.IssueToken("text")},
		}}}},
	}
	if index != "" {
		cfg["packages"].(map[string]any)["registries"] = []any{map[string]any{"name": "test", "url": index}}
	}
	return cfg
}

func listPackages(t *testing.T, base string) []packageView {
	t.Helper()
	var l packageList
	if code := manage(t, base, "GET", "/packages", nil, &l); code != http.StatusOK {
		t.Fatalf("GET /packages: status %d", code)
	}
	return l.Packages
}

// askCodex sends a Responses request for the Codex account's model.
func askCodex(t *testing.T, base string) (int, string) {
	t.Helper()
	resp := postPath(t, t.Context(), base, "/v1/responses", clientKey, `{"model":"gpt-6.1-sol","input":"Say hello"}`)
	defer resp.Body.Close()
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return resp.StatusCode, b.String()
}

func codexRunning(s coreStatus) bool {
	for _, e := range s.Extensions {
		if e.ID == "pluxy/codex" && e.State != "stopped" {
			return true
		}
	}
	return false
}

// The slice's check: while core runs and serves through the extension its config names, the
// Codex extension is installed from a registry, enabled, serves its account, and is removed
// again: its program stops, its files go, and the other extension serves throughout.
func TestInstallAndRemoveCodexWhileCoreRuns(t *testing.T) {
	u := answeringUpstream(t)
	srv := codextest.New(t)
	archive := codexArchive(t, "0.1.0")
	base, pkgDir := packagesCore(t, u, srv, writeRegistry(t, archive, ""))

	if code, body := ask(t, base, "gpt-4o"); code != http.StatusOK {
		t.Fatalf("before install: gpt-4o status %d: %s", code, body)
	}
	if code, _ := askCodex(t, base); code == http.StatusOK {
		t.Fatal("before install: the Codex model was served")
	}

	var installed struct {
		Package packageView `json:"package"`
	}
	if code := manage(t, base, "POST", "/packages", map[string]any{"id": "pluxy/codex", "enable": true}, &installed); code != http.StatusOK {
		t.Fatalf("install: status %d: %+v", code, installed)
	}
	sum := sha256.Sum256(archive)
	if p := installed.Package; p.ID != "pluxy/codex" || p.Version != "0.1.0" || !p.Enabled || p.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("installed package = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "pluxy", "codex", "0.1.0", "bin", "codex")); err != nil {
		t.Fatalf("installed program: %v", err)
	}
	waitStatus(t, base, "codex running", codexRunning)

	if code, body := askCodex(t, base); code != http.StatusOK || !strings.Contains(body, "Hello from the conformance kit.") {
		t.Fatalf("after install: Codex model status %d: %s", code, body)
	}
	if reqs := srv.Requests(); len(reqs) != 1 || reqs[0].Path != "/responses" {
		t.Fatalf("requests at the Codex upstream: %+v", reqs)
	}
	if code, body := ask(t, base, "gpt-4o"); code != http.StatusOK {
		t.Fatalf("after install: gpt-4o status %d: %s", code, body)
	}

	// With two extensions running, a login must say which extension it is for.
	var l loginReply
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "second", "method": "api_key", "input": map[string]string{"api_key": "k"}}, &l); code != http.StatusBadRequest {
		t.Errorf("login without an extension while two run: status %d, want 400", code)
	}
	if code := manage(t, base, "POST", "/accounts", map[string]any{"id": "second", "extension": "pluxy/codex", "method": "api_key", "input": map[string]string{"api_key": "k"}}, &l); code != http.StatusAccepted {
		t.Errorf("login for pluxy/codex: status %d: %+v", code, l.Login)
	}

	if code := manage(t, base, "DELETE", "/packages/pluxy/codex", nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove: status %d", code)
	}
	waitStatus(t, base, "codex stopped", func(s coreStatus) bool { return !codexRunning(s) })
	if code, _ := askCodex(t, base); code == http.StatusOK {
		t.Error("after remove: the Codex model was served")
	}
	if code, body := ask(t, base, "gpt-4o"); code != http.StatusOK {
		t.Errorf("after remove: gpt-4o status %d: %s", code, body)
	}
	if ps := listPackages(t, base); len(ps) != 0 {
		t.Errorf("packages after remove: %+v", ps)
	}
	// The files go once the drained program has stopped.
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(pkgDir, "pluxy"))
		if os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("package files after remove: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An archive whose digest is not the one the registry pins is refused, and nothing is
// installed.
func TestInstallRefusesArchiveWithWrongDigest(t *testing.T) {
	u := answeringUpstream(t)
	srv := codextest.New(t)
	archive := codexArchive(t, "0.1.0")
	wrong := sha256.Sum256(append([]byte("not "), archive...))
	base, pkgDir := packagesCore(t, u, srv, writeRegistry(t, archive, hex.EncodeToString(wrong[:])))

	var reply struct {
		Error struct{ Code, Message string } `json:"error"`
	}
	if code := manage(t, base, "POST", "/packages", map[string]any{"id": "pluxy/codex", "enable": true}, &reply); code != http.StatusUnprocessableEntity {
		t.Fatalf("install: status %d, want 422: %+v", code, reply)
	}
	if !strings.Contains(reply.Error.Message, "digest") {
		t.Errorf("error does not name the digest: %q", reply.Error.Message)
	}
	if ps := listPackages(t, base); len(ps) != 0 {
		t.Errorf("packages: %+v", ps)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "pluxy")); !os.IsNotExist(err) {
		t.Errorf("package files: %v", err)
	}
	if s := status(t, base); codexRunning(s) {
		t.Errorf("codex runs: %+v", s)
	}
}

// Bundled packages are installed and enabled through the same path on core's first start, and
// a removed one stays removed when core starts again.
func TestBundledPackagesInstallOnFirstStartAndStayRemoved(t *testing.T) {
	u := answeringUpstream(t)
	srv := codextest.New(t)
	bundled := writeRegistry(t, codexArchive(t, "0.1.0"), "")
	pkgDir := filepath.Join(t.TempDir(), "packages")
	cfg := packagesConfig(u, srv, "", pkgDir)

	t.Run("first start", func(t *testing.T) { // core stops when the subtest ends
		base, _, _ := runCoreArgs(t, cfg, "-bundled", bundled)
		ps := listPackages(t, base)
		if len(ps) != 1 || ps[0].ID != "pluxy/codex" || !ps[0].Enabled {
			t.Fatalf("packages on first start: %+v", ps)
		}
		if code, body := askCodex(t, base); code != http.StatusOK {
			t.Fatalf("bundled Codex: status %d: %s", code, body)
		}
		if code := manage(t, base, "DELETE", "/packages/pluxy/codex", nil, nil); code != http.StatusNoContent {
			t.Fatalf("remove: status %d", code)
		}
	})
	t.Run("second start", func(t *testing.T) {
		base, _, _ := runCoreArgs(t, cfg, "-bundled", bundled)
		if ps := listPackages(t, base); len(ps) != 0 {
			t.Errorf("packages after restart: %+v", ps)
		}
	})
}

// A package whose program changed after it was installed is not run: core starts without it,
// says why, and serves with the rest.
func TestChangedPackageIsNotRun(t *testing.T) {
	u := answeringUpstream(t)
	srv := codextest.New(t)
	pkgDir := filepath.Join(t.TempDir(), "packages")
	cfg := packagesConfig(u, srv, writeRegistry(t, codexArchive(t, "0.1.0"), ""), pkgDir)

	t.Run("install", func(t *testing.T) {
		base, _, _ := runCoreArgs(t, cfg)
		if code := manage(t, base, "POST", "/packages", map[string]any{"id": "pluxy/codex", "enable": true}, nil); code != http.StatusOK {
			t.Fatalf("install: status %d", code)
		}
	})
	prog := filepath.Join(pkgDir, "pluxy", "codex", "0.1.0", "bin", "codex")
	b, err := os.ReadFile(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prog+".new", append(b, "changed"...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(prog+".new", prog); err != nil {
		t.Fatal(err)
	}

	t.Run("start with changed files", func(t *testing.T) {
		base, _, _ := runCoreArgs(t, cfg)
		ps := listPackages(t, base)
		if len(ps) != 1 || ps[0].State != "failed" || !strings.Contains(ps[0].Error, "changed since it was installed") {
			t.Fatalf("packages: %+v", ps)
		}
		if s := status(t, base); codexRunning(s) {
			t.Errorf("the changed program runs: %+v", s)
		}
		if code, body := ask(t, base, "gpt-4o"); code != http.StatusOK {
			t.Errorf("gpt-4o status %d: %s", code, body)
		}
	})
}
