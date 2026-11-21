package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// cliResult is what one run of the pluxy command printed and how it exited.
type cliResult struct {
	stdout, stderr string
	code           int
}

// runCLI runs the built pluxy program with args and the extra environment variables env.
func runCLI(t *testing.T, env []string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), args...)
	// Only what the test names: PLUXY_URL or PLUXY_MANAGEMENT_KEY from the environment the test
	// runs in must not reach the command.
	cmd.Env = append([]string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	cmd.Dir = t.TempDir() // no config.json here
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return cliResult{stdout.String(), stderr.String(), code}
}

func (r cliResult) String() string {
	return fmt.Sprintf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
}

// The pluxy command against a running core: one command for each exit code the contract
// gives (next/management/README.md), with --json printing the API's reply body unchanged on
// standard output, and errors on standard error.
func TestCLIExitCodes(t *testing.T) {
	base := startCore(t, newUpstream(t), func(cfg map[string]any) { cfg["management_key"] = managementKey })
	target := []string{"--url", base, "--key", managementKey}
	// Core checks the account in the background after it starts, and a check is a call into the
	// extension, which /status shows as active_requests while it runs. Wait for the check to
	// finish, so the two reads of /status below see the same live state.
	waitChecks(t, base, func(states map[string]string) bool {
		state, checked := states["upstream-1"]
		return checked && state != "checking"
	})

	// 0: status --json prints GET /status's body as it is.
	r := runCLI(t, nil, append([]string{"status", "--json"}, target...)...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("status --json: %s", r)
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v0/management/status", nil)
	req.Header.Set("Authorization", "Bearer "+managementKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// The two bodies differ only in nothing: the status does not change between the calls
	// while the test makes no request, so they are compared as JSON values.
	var fromCLI, fromAPI any
	if err := json.Unmarshal([]byte(r.stdout), &fromCLI); err != nil {
		t.Fatalf("status --json printed no JSON: %v\n%s", err, r)
	}
	_ = json.Unmarshal(body, &fromAPI)
	if a, b := mustJSON(fromCLI), mustJSON(fromAPI); a != b {
		t.Errorf("status --json printed\n%s\nthe API answers\n%s", a, b)
	}
	// The same through the environment instead of flags, and for people, without --json.
	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}
	if r := runCLI(t, env, "status"); r.code != 0 || !strings.Contains(r.stdout, "pluxy/openai-compatible") {
		t.Errorf("status with PLUXY_URL and PLUXY_MANAGEMENT_KEY: %s", r)
	}

	// 1: a call that fails for another reason: core's log is not written to files here, so
	// GET /logs answers 400.
	if r := runCLI(t, env, "logs"); r.code != 1 || r.stdout != "" || r.stderr == "" {
		t.Errorf("logs with file logging off: %s; want exit 1 and the error on standard error", r)
	}

	// 2: a command line that is wrong.
	for _, args := range [][]string{{"no-such-command"}, {"status", "--no-such-flag"}, {"key", "remove"}} {
		if r := runCLI(t, env, args...); r.code != 2 {
			t.Errorf("%q: %s; want exit 2", args, r)
		}
	}

	// 3: no core answers at the address.
	stopped := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))
	if r := runCLI(t, nil, "status", "--json", "--url", stopped, "--key", managementKey); r.code != 3 || r.stdout != "" {
		t.Errorf("status with no core: %s; want exit 3 and nothing on standard output", r)
	}

	// 4: a wrong key. With --json the API's error object is on standard error.
	r = runCLI(t, nil, "status", "--json", "--url", base, "--key", "wrong-key")
	var apiErr struct {
		Error struct{ Code string } `json:"error"`
	}
	if r.code != 4 || r.stdout != "" || json.Unmarshal([]byte(r.stderr), &apiErr) != nil || apiErr.Error.Code != "unauthorized" {
		t.Errorf("status with a wrong key: %s; want exit 4 and the error object on standard error", r)
	}

	// 5: no such package.
	if r := runCLI(t, env, "ext", "enable", "nobody/nothing"); r.code != 5 {
		t.Errorf("ext enable of a package that is not installed: %s; want exit 5", r)
	}

	// 6: core refuses the change: the config's only client key cannot be removed.
	r = runCLI(t, env, "key", "list", "--json")
	var keys struct {
		Keys []struct{ ID string } `json:"keys"`
	}
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &keys) != nil || len(keys.Keys) != 1 {
		t.Fatalf("key list --json: %s", r)
	}
	if r := runCLI(t, env, "key", "remove", keys.Keys[0].ID); r.code != 6 || !strings.Contains(r.stderr, "last client key") {
		t.Errorf("removing the last client key: %s; want exit 6", r)
	}
}

// pluxy serve runs core, as pluxy with no subcommand does.
func TestCLIServe(t *testing.T) {
	u := newUpstream(t)
	cfg := map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "upstream-1",
				"settings":   map[string]any{"base_url": u.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	}
	dir := t.TempDir()
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	base, _, err := startCoreArgv(t, filepath.Join(binDir, "pluxy"), "serve", "--config", path)
	if err != nil {
		t.Fatal(err)
	}
	if r := runCLI(t, nil, "status", "--url", base, "--key", managementKey); r.code != 0 {
		t.Errorf("status of a core started with pluxy serve: %s", r)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// pluxy model refresh updates the served model list from an OpenAI-compatible upstream.
func TestCLIModelRefresh(t *testing.T) {
	var (
		mu     sync.Mutex
		models = []string{"m-cli-1"}
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			mu.Lock()
			cur := append([]string(nil), models...)
			mu.Unlock()
			var data []map[string]any
			for _, id := range cur {
				data = append(data, map[string]any{"id": id, "object": "model"})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(up.Close)

	base := runCore(t, map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id":         "upstream-1",
				"settings":   map[string]any{"base_url": up.URL + "/v1"},
				"credential": map[string]any{"kind": "api_key", "api_key": upstreamKey},
			}},
		},
	})
	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}

	mu.Lock()
	models = []string{"m-cli-1", "m-cli-2"}
	mu.Unlock()

	r := runCLI(t, env, "model", "refresh", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, "m-cli-2") {
		t.Fatalf("model refresh --json: %s", r)
	}
	if r := runCLI(t, env, "model", "list"); r.code != 0 || !strings.Contains(r.stdout, "m-cli-2") {
		t.Fatalf("model list after refresh: %s", r)
	}
	if r := runCLI(t, env, "model", "refresh", "--account", "upstream-1"); r.code != 0 || !strings.Contains(r.stdout, "upstream-1") {
		t.Fatalf("model refresh --account upstream-1: %s", r)
	}
	if r := runCLI(t, env, "model", "refresh", "--account", "nobody"); r.code != 5 {
		t.Fatalf("model refresh --account nobody: %s; want exit 5", r)
	}
}
