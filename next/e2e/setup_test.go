package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

func managedJSON(t *testing.T, base, key, method, path string, in any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+"/v0/management"+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
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

func writeFakeTool(t *testing.T, dir, name, script string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// isolatedSetupEnv is the whole environment of a core whose home is home and whose PATH holds
// fakeBin and the system's own programs.
func isolatedSetupEnv(home, fakeBin string) []string {
	return append(testHomeEnv(home), "PATH="+fakeBin+string(os.PathListSeparator)+"/usr/bin:/bin")
}

// startSetupCore starts core on a new config in a folder of its own with env, and returns its
// address, a management client for it, the config's path and a stop that ends core.
func startSetupCore(t *testing.T, env []string, cfgPath, bundledIndex string) (string, *mgmt.Client, func()) {
	t.Helper()
	if _, err := os.Stat(cfgPath); err != nil {
		cfg, _ := json.Marshal(map[string]any{
			"listen":         "127.0.0.1:0",
			"client_keys":    []string{clientKey},
			"management_key": managementKey,
		})
		if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base, _, stop, err := startCoreEnv(t, env, filepath.Join(binDir, "pluxy"), "serve", "-config", cfgPath, "-bundled", bundledIndex)
	if err != nil {
		t.Fatal(err)
	}
	return base, &mgmt.Client{URL: base, Key: managementKey}, stop
}

func waitForSetupScan(t *testing.T, c *mgmt.Client) mgmt.Setup {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		s, err := c.Setup(context.Background())
		if err != nil {
			t.Fatalf("GET /setup: %v", err)
		}
		if !s.Scanning && s.ScannedAt != nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup scan did not finish within 15s: %+v", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func clientByID(s mgmt.Setup, id string) mgmt.SetupClient {
	for _, c := range s.Found.Clients {
		if c.ID == id {
			return c
		}
	}
	return mgmt.SetupClient{}
}

func TestSetupScanKeepApplyDone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fake tools")
	}
	t.Parallel()
	home := t.TempDir()
	fakeBin := t.TempDir()
	writeFakeTool(t, fakeBin, "claude", `echo "2.4.1 (Claude Code)"`)
	// A probe that hangs must be cut at 4 s and reported without a version.
	writeFakeTool(t, fakeBin, "codex", `sleep 30`)

	// Existing Codex sign-in (file existence only; never read tokens).
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":"opaque"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Existing CLIProxyAPI setup with 2 auth files.
	cpaDir := filepath.Join(home, ".cli-proxy-api")
	if err := os.MkdirAll(cpaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"config.yaml": "port: 8317\n", "a.json": "{}", "b.json": "{}"} {
		if err := os.WriteFile(filepath.Join(cpaDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	packOut := t.TempDir()
	runPack(t, buildPackTool(t), packOut, "-extensions", "claude,codex,openai-compatible")
	cfgDir := t.TempDir()
	base, client, _ := startSetupCore(t, isolatedSetupEnv(home, fakeBin), filepath.Join(cfgDir, "config.json"), filepath.Join(packOut, "bundled", "index.json"))

	code, statusBody := managedJSON(t, base, managementKey, http.MethodGet, "/status", nil)
	var st struct {
		FirstRun bool `json:"first_run"`
	}
	if code != http.StatusOK || json.Unmarshal(statusBody, &st) != nil || !st.FirstRun {
		t.Fatalf("GET /status: %d %s; want first_run true", code, statusBody)
	}

	// The first start scans once core listens; the hanging codex probe is cut at 4 s.
	sv := waitForSetupScan(t, client)
	if !sv.FirstRun {
		t.Errorf("GET /setup first_run = false, want true")
	}
	if cc := clientByID(sv, "claude-code"); !cc.Installed || cc.Version != "2.4.1" || cc.State != "not-configured" || cc.Config != "~/.claude/settings.json" {
		t.Errorf("claude-code client = %+v", cc)
	}
	if cx := clientByID(sv, "codex"); !cx.Installed || cx.Version != "" || cx.State != "not-configured" || cx.Config != "~/.codex/config.toml" {
		t.Errorf("codex client = %+v (want installed, no version after the 4 s limit)", cx)
	}
	if len(sv.Found.SignIns) != 1 || sv.Found.SignIns[0].Provider != "pluxy/codex" {
		t.Errorf("sign_ins = %+v, want pluxy/codex", sv.Found.SignIns)
	}
	if len(sv.Found.Imports) != 1 || sv.Found.Imports[0].Kind != "cliproxyapi" || sv.Found.Imports[0].Accounts != 2 {
		t.Errorf("imports = %+v, want cliproxyapi with 2 accounts", sv.Found.Imports)
	}
	if sv.Found.Core.ReachableFromNetwork {
		t.Errorf("core.reachable_from_network = true, want false for 127.0.0.1")
	}

	// Picks order: providers in setup's order (codex, claude, openai-compatible).
	if len(sv.Picks) != 3 || sv.Picks[0].ID != "pluxy/codex" || sv.Picks[1].ID != "pluxy/claude" || sv.Picks[2].ID != "pluxy/openai-compatible" {
		t.Fatalf("picks = %+v", sv.Picks)
	}
	// Codex is signed in here, so it is suggested; Claude Code is only installed, which says
	// nothing of a Claude subscription, so Claude is not.
	if !sv.Picks[0].Suggested || sv.Picks[1].Suggested || sv.Picks[2].Suggested {
		t.Errorf("picks suggested flags: codex=%v claude=%v openai-compatible=%v, want true false false", sv.Picks[0].Suggested, sv.Picks[1].Suggested, sv.Picks[2].Suggested)
	}

	info, err := os.Stat(filepath.Join(cfgDir, "state", "setup.json"))
	if err != nil {
		t.Fatalf("state/setup.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("state/setup.json mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSetupRestartApplyAndDone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fake tools")
	}
	t.Parallel()
	home := t.TempDir()
	fakeBin := t.TempDir()
	writeFakeTool(t, fakeBin, "claude", `echo "2.4.1 (Claude Code)"`)
	writeFakeTool(t, fakeBin, "codex", `sleep 30`)
	env := isolatedSetupEnv(home, fakeBin)

	packOut := t.TempDir()
	runPack(t, buildPackTool(t), packOut, "-extensions", "claude,openai-compatible")
	bundledIndex := filepath.Join(packOut, "bundled", "index.json")
	cfgPath := filepath.Join(t.TempDir(), "config.json")

	_, client1, stop1 := startSetupCore(t, env, cfgPath, bundledIndex)
	first := waitForSetupScan(t, client1)
	stop1()

	// After a restart GET /setup returns the kept scan at once, and core does not scan again
	// on its own.
	_, client, _ := startSetupCore(t, env, cfgPath, bundledIndex)
	start := time.Now()
	kept, err := client.Setup(context.Background())
	if err != nil {
		t.Fatalf("GET /setup after restart: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("GET /setup after restart took %v, want the kept scan at once", elapsed)
	}
	if kept.Scanning || kept.ScannedAt == nil || !kept.ScannedAt.Equal(*first.ScannedAt) {
		t.Errorf("after restart: scanning %v, scanned_at %v; want the kept scan of %v", kept.Scanning, kept.ScannedAt, first.ScannedAt)
	}
	time.Sleep(500 * time.Millisecond)
	if again, err := client.Setup(context.Background()); err != nil || again.Scanning || !again.ScannedAt.Equal(*first.ScannedAt) {
		t.Errorf("core scanned again on its own after a restart: %+v (%v)", again, err)
	}

	// POST /setup/scan returns at once with scanning: true, and scans.
	scanning, err := client.SetupScan(context.Background())
	if err != nil || !scanning.Scanning {
		t.Fatalf("POST /setup/scan = %+v (%v); want scanning: true", scanning, err)
	}
	if rescanned := waitForSetupScan(t, client); !rescanned.ScannedAt.After(*first.ScannedAt) {
		t.Errorf("POST /setup/scan: scanned_at %v, not after %v", rescanned.ScannedAt, first.ScannedAt)
	}

	// POST /setup/apply installs pluxy/claude from the bundled registry even when another pick fails.
	applied, err := client.SetupApply(context.Background(), []string{"pluxy/claude", "nobody/missing"})
	if err != nil || len(applied.Results) != 2 {
		t.Fatalf("POST /setup/apply = %+v (%v)", applied, err)
	}
	if r := applied.Results[0]; r.ID != "pluxy/claude" || r.Status != "done" {
		t.Errorf("result[0] = %+v", r)
	}
	if r := applied.Results[1]; r.ID != "nobody/missing" || r.Status != "error" || r.Error == "" {
		t.Errorf("result[1] = %+v", r)
	}
	pkgs, err := client.Packages(context.Background())
	if err != nil || !slices.ContainsFunc(pkgs, func(p mgmt.Package) bool { return p.ID == "pluxy/claude" }) {
		t.Errorf("GET /packages = %+v (%v); want pluxy/claude", pkgs, err)
	}

	// POST /setup/done makes first_run false in GET /setup and GET /status.
	if done, err := client.SetupDone(context.Background()); err != nil || done.FirstRun {
		t.Errorf("POST /setup/done = %+v (%v)", done, err)
	}
	if s, err := client.Setup(context.Background()); err != nil || s.FirstRun {
		t.Errorf("GET /setup after done: first_run %v (%v)", s.FirstRun, err)
	}
	if s, err := client.Status(context.Background()); err != nil || s.FirstRun {
		t.Errorf("GET /status after done: first_run %v (%v)", s.FirstRun, err)
	}
}

func TestCLISetupAndClientShow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fake tools")
	}
	t.Parallel()
	home := t.TempDir()
	fakeBin := t.TempDir()
	writeFakeTool(t, fakeBin, "claude", `echo "2.4.1 (Claude Code)"`)
	// A Claude Code sign-in (only its existence is seen), which is what suggests the Claude pick.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":"opaque"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isolatedSetupEnv(home, fakeBin)

	packOut := t.TempDir()
	runPack(t, buildPackTool(t), packOut, "-extensions", "claude,openai-compatible")
	base, client, _ := startSetupCore(t, env, filepath.Join(t.TempDir(), "config.json"), filepath.Join(packOut, "bundled", "index.json"))
	waitForSetupScan(t, client)

	cliEnv := append(slices.Clone(env), "PLUXY_URL="+base, "PLUXY_MANAGEMENT_KEY="+managementKey)

	r := runCLI(t, cliEnv, "setup", "--json")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("pluxy setup --json: %s", r)
	}
	var sv mgmt.Setup
	if err := json.Unmarshal([]byte(r.stdout), &sv); err != nil {
		t.Fatalf("pluxy setup --json invalid JSON: %v\n%s", err, r)
	}
	if len(sv.Picks) < 2 || sv.Picks[0].ID != "pluxy/claude" || !sv.Picks[0].Suggested {
		t.Errorf("pluxy setup --json picks = %+v", sv.Picks)
	}

	// In words, one line on standard error names the next step: here, the suggested pick.
	r = runCLI(t, cliEnv, "setup")
	if r.code != 0 || r.stderr != "Next: pluxy setup apply --yes installs what core suggests (pluxy/claude).\n" || strings.Contains(r.stdout, "Next:") {
		t.Errorf("pluxy setup: %s", r)
	}

	r = runCLI(t, cliEnv, "client", "show", "claude-code")
	// This core serves no model, so the text also holds the model placeholder and says so.
	if r.code != 0 || r.stderr != "Replace <client-key> with a client key: core's config file holds them, and \"pluxy key add\" makes another.\nReplace <model-id> with the name of a model your accounts serve: \"pluxy model list\" lists them.\n" {
		t.Fatalf("pluxy client show claude-code: %s", r)
	}
	if !strings.Contains(r.stdout, base) || !strings.Contains(r.stdout, "ANTHROPIC_BASE_URL") || !strings.Contains(r.stdout, "ANTHROPIC_AUTH_TOKEN") {
		t.Errorf("pluxy client show claude-code output missing config values:\n%s", r.stdout)
	}
	if short := runCLI(t, cliEnv, "client", "show", "claude"); short.code != 0 || short.stdout != r.stdout {
		t.Errorf("pluxy client show claude is not claude-code's:\n%s", short)
	}
	if strings.Contains(r.stdout, managementKey) {
		t.Errorf("pluxy client show printed the management key!")
	}

	// Look up the client key ID via key list --json and pass --key-id.
	kr := runCLI(t, cliEnv, "key", "list", "--json")
	var keys struct {
		Keys []struct {
			ID     string `json:"id"`
			Masked string `json:"masked"`
		} `json:"keys"`
	}
	if kr.code != 0 || json.Unmarshal([]byte(kr.stdout), &keys) != nil || len(keys.Keys) == 0 {
		t.Fatalf("key list --json: %s", kr)
	}
	r = runCLI(t, cliEnv, "client", "show", "claude-code", "--key-id", keys.Keys[0].ID)
	if r.code != 0 || !strings.Contains(r.stdout, keys.Keys[0].ID) || strings.Contains(r.stdout, managementKey) || strings.Contains(r.stdout, clientKey) {
		t.Errorf("client show --key-id: %s", r)
	}
	if want := "Replace <client-key:" + keys.Keys[0].ID + " (" + keys.Keys[0].Masked + ")> with that key's full value"; !strings.HasPrefix(r.stderr, want) {
		t.Errorf("client show --key-id stderr: %q, want it to start %q", r.stderr, want)
	}

	// pluxy setup --yes --json applies the suggested picks and prints the apply reply.
	r = runCLI(t, cliEnv, "setup", "--yes", "--json")
	var reply mgmt.SetupApplyReply
	if r.code != 0 || json.Unmarshal([]byte(r.stdout), &reply) != nil || len(reply.Results) == 0 || reply.Results[0].ID != "pluxy/claude" || reply.Results[0].Status != "done" {
		t.Errorf("pluxy setup --yes --json: %s", r)
	}
	// The provider runs and core has no account: the next step is to add one, after pluxy setup
	// and after an apply that had nothing left to install.
	const addAccount = "Next: add an account with pluxy account add pluxy/claude (pluxy account methods lists the ways to sign in).\n"
	if r = runCLI(t, cliEnv, "setup"); r.code != 0 || r.stderr != addAccount {
		t.Errorf("pluxy setup after the pick was applied: %s", r)
	}
	if r = runCLI(t, cliEnv, "setup", "apply", "--yes"); r.code != 0 || r.stdout != "no suggested picks to apply\n" || r.stderr != addAccount {
		t.Errorf("pluxy setup apply --yes with nothing suggested: %s", r)
	}
	// A pick applied in words is followed by the step for the state it leaves.
	if r = runCLI(t, cliEnv, "setup", "apply", "--pick", "pluxy/openai-compatible"); r.code != 0 || !strings.Contains(r.stdout, "pluxy/openai-compatible  done") ||
		r.stderr != "Next: add an account with pluxy account add <provider> (pluxy account methods lists the providers and their ways to sign in).\n" {
		t.Errorf("pluxy setup apply --pick: %s", r)
	}
	// Nothing left to apply: with --json, a usage error as an error object.
	r = runCLI(t, cliEnv, "setup", "apply", "--yes", "--json")
	if r.code != 2 || r.stdout != "" || !strings.HasPrefix(r.stderr, `{"error":{"code":"invalid_request"`) {
		t.Errorf("pluxy setup apply --yes --json with nothing suggested: %s", r)
	}
	r = runCLI(t, cliEnv, "setup", "done", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"first_run":false`) {
		t.Errorf("pluxy setup done --json: %s", r)
	}
	// Setup has ended: no next step is named.
	if r = runCLI(t, cliEnv, "setup"); r.code != 0 || r.stderr != "" || !strings.HasPrefix(r.stdout, "first_run: false") {
		t.Errorf("pluxy setup after setup done: %s", r)
	}
}

// GET /setup finds Claude Desktop, Grok Build and Hermes Agent where the desktop app looks for
// them, and reports each as not installed when they are not there. Claude Desktop is never run
// (it would open its window), so it has no version.
func TestSetupScansDesktopTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fake tools")
	}
	t.Parallel()
	tools := []struct{ id, name string }{
		{"claude-desktop", "Claude Desktop"},
		{"grok-build", "Grok Build"},
		{"hermes", "Hermes Agent"},
	}

	t.Run("installed", func(t *testing.T) {
		home := t.TempDir()
		fakeBin := t.TempDir()
		ran := filepath.Join(t.TempDir(), "claude-desktop-ran")
		if runtime.GOOS == "darwin" {
			// An application bundle in the home folder's Applications.
			if err := os.MkdirAll(filepath.Join(home, "Applications", "Claude.app"), 0o755); err != nil {
				t.Fatal(err)
			}
		} else {
			writeFakeTool(t, fakeBin, "claude-desktop", `echo ran >> `+ran+`; echo "9.9.9"`)
		}
		// Grok Build and Hermes Agent are in the folders their installers use, not on the PATH.
		for rel, script := range map[string]string{
			filepath.Join(".grok", "bin", "grok"):    `echo "grok 0.4.2"`,
			filepath.Join(".local", "bin", "hermes"): `echo "Hermes Agent v1.2.3"`,
		} {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(home, rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFakeTool(t, home, rel, script)
		}

		_, client, _ := startSetupCore(t, isolatedSetupEnv(home, fakeBin), filepath.Join(t.TempDir(), "config.json"), "")
		sv := waitForSetupScan(t, client)
		want := map[string]struct{ version, config string }{
			"claude-desktop": {"", map[string]string{
				"darwin": "~/Library/Application Support/Claude-3p/claude_desktop_config.json",
			}[runtime.GOOS]},
			"grok-build": {"0.4.2", "~/.grok/config.toml"},
			"hermes":     {"1.2.3", "~/.hermes/config.yaml"},
		}
		if w := want["claude-desktop"]; w.config == "" {
			want["claude-desktop"] = struct{ version, config string }{"", "~/.config/Claude-3p/claude_desktop_config.json"}
		}
		for _, tool := range tools {
			c := clientByID(sv, tool.id)
			w := want[tool.id]
			if c.ID != tool.id || c.Name != tool.name || !c.Installed || c.Version != w.version || c.Config != w.config || c.State != "unchecked" {
				t.Errorf("%s = %+v, want installed, version %q, config %q, unchecked", tool.id, c, w.version, w.config)
			}
		}
		if _, err := os.Stat(ran); err == nil {
			t.Error("the scan ran claude-desktop, which opens its window")
		}
	})

	t.Run("absent", func(t *testing.T) {
		// A PATH that holds only the fake folder and the system's, and a home with no tool in it.
		_, client, _ := startSetupCore(t, isolatedSetupEnv(t.TempDir(), t.TempDir()), filepath.Join(t.TempDir(), "config.json"), "")
		sv := waitForSetupScan(t, client)
		for _, tool := range tools {
			c := clientByID(sv, tool.id)
			if c.ID != tool.id || c.Name != tool.name || c.Installed || c.Version != "" || c.State != "unchecked" {
				t.Errorf("%s = %+v, want listed, not installed and unchecked", tool.id, c)
			}
		}
	})
}
