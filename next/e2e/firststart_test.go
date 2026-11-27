package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// registriesView is GET /registries's reply, the part these tests read.
type registriesView struct {
	Registries []struct {
		Name     string `json:"name"`
		Error    string `json:"error"`
		Packages []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Summary string `json:"summary"`
			Kind    string `json:"kind"`
			Tier    string `json:"tier"`
		} `json:"packages"`
	} `json:"registries"`
}

// managed sends a management call with key and returns the status and the body.
func managed(t *testing.T, base, key, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+"/v0/management"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// A new install starts with no config: core writes one (on this machine only, both keys, only
// its user can read it) and says where, installs nothing, and offers what the bundled registry
// holds, described.
func TestFirstStartWritesConfigAndInstallsNothing(t *testing.T) {
	// Never 8317: a core of the person running the tests may listen there.
	t.Setenv("PLUXY_INIT_PORT", "0")
	out := t.TempDir()
	runPack(t, buildPackTool(t), out, "-extensions", "claude,openai-compatible")
	path := filepath.Join(t.TempDir(), "config.json")

	base, logs, err := startCoreArgv(t, filepath.Join(binDir, "pluxy"), "serve", "-config", path,
		"-bundled", filepath.Join(out, "bundled", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	all := logs.waitFor(t, "first start: wrote a new config, "+path)
	// Once it listens it says where to go on: the panel when this build has one, and the command
	// with what it needs to reach this core.
	next := "pluxy: first-run setup is not finished. Next: open " + base + "/panel/ in a browser and sign in with the management key, or run pluxy setup --config " + path
	if _, err := os.Stat(filepath.Join("..", "internal", "panel", "dist", "index.html")); err != nil {
		next = "pluxy: first-run setup is not finished. Next: run pluxy setup --config " + path
	}
	if !slices.Contains(strings.Split(logs.waitFor(t, "first-run setup"), "\n"), next) {
		t.Errorf("the start's log has no line %q", next)
	}
	// The panel's address is this computer's own: the start says so, and how to open the panel from another computer.
	if _, err := os.Stat(filepath.Join("..", "internal", "panel", "dist", "index.html")); err == nil {
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
		want := `pluxy: that address works only on this computer. To open the panel from another computer, set "listen" to "0.0.0.0:` + port + `" and "management_allow_remote" to true in ` + path + ` (Config in the panel sets both), restart pluxy, then open http://<this computer's address>:` + port + `/panel/ there`
		if !slices.Contains(strings.Split(logs.waitFor(t, "works only on this computer"), "\n"), want) {
			t.Errorf("the start's log has no line %q", want)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600", info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Listen        string   `json:"listen"`
		ClientKeys    []string `json:"client_keys"`
		ManagementKey string   `json:"management_key"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil || host != "127.0.0.1" || port == "0" || port == "8317" {
		t.Errorf("listen %q: want 127.0.0.1 on the free port the test asked for", cfg.Listen)
	}
	if !strings.HasSuffix(base, "://"+cfg.Listen) {
		t.Errorf("core listens on %s, the config says %s", base, cfg.Listen)
	}
	if len(cfg.ClientKeys) != 1 || len(cfg.ClientKeys[0]) < 20 || len(cfg.ManagementKey) < 20 || cfg.ClientKeys[0] == cfg.ManagementKey {
		t.Errorf("keys: want one client key and a different management key, each long and random: %s", b)
	}
	for _, k := range append(cfg.ClientKeys, cfg.ManagementKey) {
		if strings.Contains(all, k) {
			t.Errorf("core's log holds a key")
		}
	}

	if code, body := managed(t, base, cfg.ManagementKey, "GET", "/status"); code != http.StatusOK {
		t.Fatalf("status with the written management key: %d %s", code, body)
	}
	code, body := managed(t, base, cfg.ManagementKey, "GET", "/packages")
	if code != http.StatusOK || !strings.Contains(string(body), `"packages":[]`) {
		t.Errorf("packages on a first start: %d %s; want none", code, body)
	}
	code, body = managed(t, base, cfg.ManagementKey, "GET", "/registries")
	var regs registriesView
	if code != http.StatusOK || json.Unmarshal(body, &regs) != nil || len(regs.Registries) != 1 {
		t.Fatalf("registries: %d %s", code, body)
	}
	got := map[string]string{}
	for _, p := range regs.Registries[0].Packages {
		if p.Title == "" || p.Summary == "" {
			t.Errorf("%s has no title or summary: %+v", p.ID, p)
		}
		got[p.ID] = p.Kind + " " + p.Tier
	}
	if regs.Registries[0].Name != "bundled" || len(got) != 2 || got["pluxy/claude"] != "provider pick" || got["pluxy/openai-compatible"] != "provider pick" {
		t.Errorf("bundled registry: %s", body)
	}
}

// A config with no management key is refused at start, with a message that says how to add one:
// the management API is never silently off.
func TestConfigWithoutManagementKeyIsRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, body, want string }{
		{"config.json", `{"listen": "127.0.0.1:0", "client_keys": ["` + clientKey + `"]}`, `"management_key"`},
		{"config.yaml", "port: 0\napi-keys: [" + clientKey + "]\n", "remote-management: {secret-key:"},
		{"hashed.yaml", "port: 0\napi-keys: [" + clientKey + "]\nremote-management: {secret-key: \"$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234\"}\n",
			"replace it with a new key (pluxy cannot use a bcrypt hash)"},
	} {
		path := filepath.Join(t.TempDir(), c.name)
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), "serve", "-config", path).CombinedOutput()
		cancel()
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatalf("%s: core did not exit with an error: %v: %s", c.name, err, out)
		}
		wants := []string{c.want}
		if c.name != "hashed.yaml" {
			wants = append(wants, "no management key", "pluxy init")
		}
		for _, want := range wants {
			if !strings.Contains(string(out), want) {
				t.Errorf("%s: message does not say %q: %s", c.name, want, out)
			}
		}
		if strings.Contains(string(out), "-safe-mode") {
			t.Errorf("%s: message points at safe mode, which fails the same way: %s", c.name, out)
		}
	}
}

// pluxy init writes the same first config without starting core, and never replaces one.
func TestInitWritesFirstConfigOnce(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "core", "config.json")
	bin := filepath.Join(binDir, "pluxy")
	out, err := exec.Command(bin, "init", "-config", path, "-port", "0").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "wrote "+path) {
		t.Fatalf("init: %v: %s", err, out)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ManagementKey string `json:"management_key"`
	}
	if err := json.Unmarshal(first, &cfg); err != nil || cfg.ManagementKey == "" {
		t.Fatalf("%v: %s", err, first)
	}
	if strings.Contains(string(out), cfg.ManagementKey) || strings.Contains(string(first), ":8317\"") {
		t.Errorf("init printed the key, or listens on 8317: %s", out)
	}
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600", info.Mode().Perm())
	}
	out, err = exec.Command(bin, "init", "-config", path, "-port", "0").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "already exists. Start core with: pluxy serve -config "+path) {
		t.Errorf("init over a config: %v: %s", err, out)
	}
	if again, _ := os.ReadFile(path); string(again) != string(first) {
		t.Error("init replaced the config")
	}
	// A port out of range is a wrong command line.
	other := filepath.Join(t.TempDir(), "config.json")
	err = exec.Command(bin, "init", "-config", other, "-port", "70000").Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
		t.Errorf("init -port 70000: %v, want exit status 2", err)
	}
	if _, err := os.Stat(other); err == nil {
		t.Error("init -port 70000 wrote a config")
	}
	// The config it wrote starts core.
	base, _ := runCoreFile(t, path)
	if code, body := managed(t, base, cfg.ManagementKey, "GET", "/status"); code != http.StatusOK {
		t.Errorf("status: %d %s", code, body)
	}
}

// With listen on an unspecified address (0.0.0.0), core's startup lines name the address as the
// config wrote it with plain words and point the first-run open line at 127.0.0.1, not [::] or [::1].
func TestFirstStartUnspecifiedListenSaysPlainAddress(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, map[string]any{
		"listen":         "0.0.0.0:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	base, logs, err := startCoreArgv(t, filepath.Join(binDir, "pluxy"), "serve", "-config", path, "-watch-interval", "0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("listening base %q: %v", base, err)
	}
	all := logs.waitFor(t, "first-run setup")
	lines := strings.Split(all, "\n")

	wantListen := "pluxy: listening on http://0.0.0.0:" + port + " (every address of this computer)"
	if !slices.Contains(lines, wantListen) {
		t.Errorf("log has no line %q:\n%s", wantListen, all)
	}
	wantNext := "pluxy: first-run setup is not finished. Next: open http://127.0.0.1:" + port + "/panel/ in a browser and sign in with the management key, or run pluxy setup --config " + path + " --url http://127.0.0.1:" + port
	if _, err := os.Stat(filepath.Join("..", "internal", "panel", "dist", "index.html")); err != nil {
		wantNext = "pluxy: first-run setup is not finished. Next: run pluxy setup --config " + path + " --url http://127.0.0.1:" + port
	}
	if !slices.Contains(lines, wantNext) {
		t.Errorf("log has no line %q:\n%s", wantNext, all)
	}
	if strings.Contains(all, "[::]") || strings.Contains(all, "[::1]") {
		t.Errorf("log printed raw IPv6 unspecified/loopback form for 0.0.0.0:\n%s", all)
	}
}
