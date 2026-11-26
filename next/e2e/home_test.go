package e2e

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pluxyHome is an environment whose pluxy's home is a folder of the test's own: HOME and
// XDG_DATA_HOME set, and PLUXY_INIT_PORT so a first start never takes 8317 on the machine.
type pluxyHome struct {
	env     []string
	homeCfg string // $XDG_DATA_HOME/pluxy/config.json, written out from the documented rule
	appCfg  string // the desktop app's own core's config
}

func newPluxyHome(t *testing.T) pluxyHome {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	return pluxyHome{
		env:     []string{"HOME=" + root, "XDG_DATA_HOME=" + data, "PLUXY_INIT_PORT=0", "PATH=" + os.Getenv("PATH")},
		homeCfg: filepath.Join(data, "pluxy", "config.json"),
		appCfg:  filepath.Join(data, "dev.pluxy.desktop", "core", "config.json"),
	}
}

// run runs the built pluxy in the folder dir with only the home's environment and extra.
func (h pluxyHome) run(t *testing.T, dir string, extra []string, args ...string) cliResult {
	t.Helper()
	return runCLIIn(t, dir, append(append([]string{}, h.env...), extra...), args...)
}

func managementKeyIn(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ManagementKey string `json:"management_key"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.ManagementKey == "" {
		t.Fatalf("%s: no management key: %v", path, err)
	}
	return cfg.ManagementKey
}

// A person with a pluxy can use the pluxy command from any folder: pluxy init writes pluxy's
// home, a plain pluxy serve in another folder serves it, and the client commands find it from a
// third, with --verbose naming it; the management key can be found and rotated from a terminal,
// and the commands go on working with the new key.
func TestPluxysHomeFromAnyFolder(t *testing.T) {
	h := newPluxyHome(t)
	r := h.run(t, t.TempDir(), nil, "init", "-port", "0")
	if r.code != 0 || !strings.Contains(r.stdout, "wrote "+h.homeCfg) {
		t.Fatalf("init: %s", r)
	}
	serveDir := t.TempDir()
	base, logs, _, err := startCoreDir(t, h.env, serveDir, filepath.Join(binDir, "pluxy"), "serve", "-watch-interval", "0")
	if err != nil {
		t.Fatal(err)
	}
	if !logs.contains("config: " + h.homeCfg + " (pluxy's home)") {
		t.Errorf("serve did not say it serves pluxy's home's config")
	}
	if _, err := os.Stat(filepath.Join(serveDir, "config.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("serve wrote a config.json in the folder it ran in: %v", err)
	}

	elsewhere := t.TempDir()
	if r := h.run(t, elsewhere, nil, "status"); r.code != 0 {
		t.Fatalf("status from another folder: %s", r)
	}
	if r := h.run(t, elsewhere, nil, "status", "--verbose"); r.code != 0 || !strings.Contains(r.stderr, "core at "+base+", its address and management key from "+h.homeCfg+" (pluxy's home)") {
		t.Errorf("status --verbose: %s", r)
	}

	oldKey := managementKeyIn(t, h.homeCfg)
	if r := h.run(t, elsewhere, nil, "key", "management"); r.code != 0 || !strings.Contains(r.stdout, h.homeCfg) || strings.Contains(r.stdout+r.stderr, oldKey) {
		t.Errorf("key management: %s", r)
	}
	if r := h.run(t, elsewhere, nil, "key", "management", "--show-key"); r.code != 0 || r.stdout != oldKey+"\n" {
		t.Errorf("key management --show-key: %s", r)
	}
	r = h.run(t, elsewhere, nil, "key", "rotate-management", "--yes")
	newKey := managementKeyIn(t, h.homeCfg)
	if r.code != 0 || r.stdout != "" || newKey == oldKey || strings.Contains(r.stderr, newKey) {
		t.Fatalf("rotate: %s (the file's key changed: %v)", r, newKey != oldKey)
	}
	if r := h.run(t, elsewhere, nil, "status"); r.code != 0 {
		t.Errorf("status after the rotation: %s", r)
	}
	if r := h.run(t, elsewhere, []string{"PLUXY_MANAGEMENT_KEY=" + oldKey}, "status"); r.code != 4 {
		t.Errorf("status with the old key: %s, want exit 4", r)
	}

	// A folder with its own config.json is read first, as before, and --verbose says the home's
	// config was not.
	own := t.TempDir()
	if err := os.WriteFile(filepath.Join(own, "config.json"), []byte(`{"listen": "127.0.0.1:1", "client_keys": ["fake-client-key-1"], "management_key": "fake-folder-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = h.run(t, own, nil, "status", "--verbose")
	if r.code != 3 || !strings.Contains(r.stderr, "config.json in this folder") || !strings.Contains(r.stderr, "pluxy's home has a config too, "+h.homeCfg) {
		t.Errorf("status in a folder with its own config.json: %s", r)
	}
	// A plain pluxy serve in such a folder serves that config.json, not pluxy's home's.
	ownServe := t.TempDir()
	if err := os.WriteFile(filepath.Join(ownServe, "config.json"), []byte(`{"listen": "127.0.0.1:0", "client_keys": ["fake-client-key-1"], "management_key": "fake-folder-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, logs, _, err = startCoreDir(t, h.env, ownServe, filepath.Join(binDir, "pluxy"), "serve", "-watch-interval", "0", "-safe-mode")
	if err != nil {
		t.Fatal(err)
	}
	if !logs.contains("config: " + filepath.Join(ownServe, "config.json") + " (config.json in the current folder)") {
		t.Errorf("serve in a folder with its own config.json did not serve it")
	}
}

// With no config anywhere a client command says where it looked and what to run; with only the
// desktop app's own core, a plain pluxy serve writes nothing and says why, and the client
// command names the app's config.
func TestPluxysHomeNotFound(t *testing.T) {
	h := newPluxyHome(t)
	r := h.run(t, t.TempDir(), nil, "status")
	if r.code != 1 || !strings.Contains(r.stderr, "no config.json in this folder") || !strings.Contains(r.stderr, h.homeCfg) || !strings.Contains(r.stderr, "pluxy serve") {
		t.Errorf("status with no config anywhere: %s", r)
	}

	if err := os.MkdirAll(filepath.Dir(h.appCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.appCfg, []byte(`{"listen": "127.0.0.1:1", "client_keys": ["fake-client-key-1"], "management_key": "fake-app-core-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r = h.run(t, t.TempDir(), nil, "serve")
	if r.code != 1 || !strings.Contains(r.stderr, "desktop app keeps a pluxy of its own") || !strings.Contains(r.stderr, h.appCfg) || !strings.Contains(r.stderr, "none was written") {
		t.Errorf("serve beside the app's own core: %s", r)
	}
	if _, err := os.Stat(filepath.Dir(h.homeCfg)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused serve made pluxy's home: %v", err)
	}
	r = h.run(t, t.TempDir(), nil, "status")
	if r.code != 1 || !strings.Contains(r.stderr, "--config "+h.appCfg) || !strings.Contains(r.stderr, "pluxy init") {
		t.Errorf("status beside the app's own core: %s", r)
	}
	// The serve just refused: the advice offers pluxy serve only with the app's -config.
	if strings.Count(r.stderr, "pluxy serve") != strings.Count(r.stderr, "pluxy serve -config ") {
		t.Errorf("status beside the app's own core offers a plain pluxy serve, which refuses: %s", r)
	}
	r = h.run(t, t.TempDir(), nil, "key", "management")
	if r.code != 5 || r.stdout != "" || !strings.Contains(r.stderr, h.homeCfg) || !strings.Contains(r.stderr, "--config "+h.appCfg) {
		t.Errorf("key management beside the app's own core: %s", r)
	}
}
