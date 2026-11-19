package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type setupView struct {
	FirstRun  bool       `json:"first_run"`
	Scanning  bool       `json:"scanning"`
	ScannedAt *time.Time `json:"scanned_at"`
	Found     struct {
		Clients []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Installed bool   `json:"installed"`
			Version   string `json:"version"`
			Config    string `json:"config"`
			State     string `json:"state"`
		} `json:"clients"`
		SignIns []struct {
			Provider string `json:"provider"`
			From     string `json:"from"`
			Label    string `json:"label"`
		} `json:"sign_ins"`
		Imports []struct {
			Kind     string `json:"kind"`
			Config   string `json:"config"`
			AuthDir  string `json:"auth_dir"`
			Accounts int    `json:"accounts"`
		} `json:"imports"`
		Core struct {
			Listen               string `json:"listen"`
			ReachableFromNetwork bool   `json:"reachable_from_network"`
		} `json:"core"`
	} `json:"found"`
	Picks []struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Title     string `json:"title"`
		Summary   string `json:"summary"`
		Installed bool   `json:"installed"`
		Suggested bool   `json:"suggested"`
		Because   string `json:"because"`
	} `json:"picks"`
}

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
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fake tools")
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func isolatedSetupEnv(home, fakeBin string) []string {
	xdg := filepath.Join(home, ".xdg")
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(xdg, "config"),
		"XDG_DATA_HOME=" + filepath.Join(xdg, "data"),
		"XDG_STATE_HOME=" + filepath.Join(xdg, "state"),
		"XDG_CACHE_HOME=" + filepath.Join(xdg, "cache"),
		"PATH=" + fakeBin + string(os.PathListSeparator) + "/usr/bin:/bin",
	}
}

func waitForSetupScan(t *testing.T, base, key string) setupView {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		code, body := managedJSON(t, base, key, http.MethodGet, "/setup", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /setup: %d %s", code, body)
		}
		var v setupView
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("GET /setup JSON: %v: %s", err, body)
		}
		if !v.Scanning && v.ScannedAt != nil {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup scan did not finish within 15s: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func startCoreEnvStoppable(t *testing.T, env []string, bin string, argv ...string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, argv...)
	if env != nil {
		cmd.Env = env
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	addr := make(chan string, 1)
	w := &lineWriter{fn: func(line string) {
		t.Logf("core: %.4096s", line)
		if rest, ok := strings.CutPrefix(line, "pluxy: listening on "); ok {
			select {
			case addr <- rest:
			default:
			}
		}
	}}
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		w.flush()
		close(addr)
		close(exited)
	}()
	stop := func() {
		http.DefaultClient.CloseIdleConnections()
		cancel()
		<-exited
	}
	t.Cleanup(stop)
	select {
	case u, ok := <-addr:
		if !ok {
			t.Fatal("core exited before listening")
		}
		return u, stop
	case <-time.After(15 * time.Second):
		t.Fatal("core did not listen within 15s")
		return "", stop
	}
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
	if err := os.WriteFile(filepath.Join(cpaDir, "config.yaml"), []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cpaDir, "a.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cpaDir, "b.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := isolatedSetupEnv(home, fakeBin)

	packOut := t.TempDir()
	runPack(t, buildPackTool(t), packOut, "-extensions", "claude,codex,openai-compatible")
	bundledIndex := filepath.Join(packOut, "bundled", "index.json")

	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfgBytes, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	base, stop := startCoreEnvStoppable(t, env, filepath.Join(binDir, "pluxy"), "serve", "-config", cfgPath, "-bundled", bundledIndex)
	defer stop()

	// GET /status has first_run: true.
	code, statusBody := managedJSON(t, base, managementKey, http.MethodGet, "/status", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /status: %d %s", code, statusBody)
	}
	var st struct {
		FirstRun bool `json:"first_run"`
	}
	if err := json.Unmarshal(statusBody, &st); err != nil || !st.FirstRun {
		t.Fatalf("GET /status first_run = %v (%v): %s", st.FirstRun, err, statusBody)
	}

	// Scan finishes (hanging codex probe is cut at 4 s, well under 15 s).
	sv := waitForSetupScan(t, base, managementKey)
	if !sv.FirstRun {
		t.Errorf("GET /setup first_run = false, want true")
	}

	clients := map[string]struct {
		installed bool
		version   string
		config    string
		state     string
	}{}
	for _, c := range sv.Found.Clients {
		clients[c.ID] = struct {
			installed bool
			version   string
			config    string
			state     string
		}{c.Installed, c.Version, c.Config, c.State}
	}
	if cc := clients["claude-code"]; !cc.installed || cc.version != "2.4.1" || cc.state != "not-configured" || cc.config != "~/.claude/settings.json" {
		t.Errorf("claude-code client = %+v", cc)
	}
	if cx := clients["codex"]; !cx.installed || cx.version != "" || cx.state != "not-configured" || cx.config != "~/.codex/config.toml" {
		t.Errorf("codex client = %+v (want installed=true, empty version after 4s timeout)", cx)
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

	// Picks order: providers in plan order (codex, claude, openai-compatible).
	if len(sv.Picks) != 3 || sv.Picks[0].ID != "pluxy/codex" || sv.Picks[1].ID != "pluxy/claude" || sv.Picks[2].ID != "pluxy/openai-compatible" {
		t.Fatalf("picks = %+v", sv.Picks)
	}
	if !sv.Picks[0].Suggested || !sv.Picks[1].Suggested || sv.Picks[2].Suggested {
		t.Errorf("picks suggested flags: codex=%v claude=%v openai-compatible=%v", sv.Picks[0].Suggested, sv.Picks[1].Suggested, sv.Picks[2].Suggested)
	}

	// state/setup.json exists with mode 0600.
	statePath := filepath.Join(cfgDir, "state", "setup.json")
	info, err := os.Stat(statePath)
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

	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfgBytes, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	base1, stop1 := startCoreEnvStoppable(t, env, filepath.Join(binDir, "pluxy"), "serve", "-config", cfgPath, "-bundled", bundledIndex)
	sv1 := waitForSetupScan(t, base1, managementKey)
	if sv1.ScannedAt == nil {
		t.Fatal("scanned_at is nil")
	}
	firstScannedAt := *sv1.ScannedAt
	stop1()

	// Restart core: GET /setup returns the kept scan at once (scanned_at unchanged, before the 4s scan finishes).
	base2, stop2 := startCoreEnvStoppable(t, env, filepath.Join(binDir, "pluxy"), "serve", "-config", cfgPath, "-bundled", bundledIndex)
	defer stop2()

	start := time.Now()
	code, body := managedJSON(t, base2, managementKey, http.MethodGet, "/setup", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /setup after restart: %d %s", code, body)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("GET /setup after restart took %v, want immediate kept scan", elapsed)
	}
	var kept setupView
	if err := json.Unmarshal(body, &kept); err != nil {
		t.Fatal(err)
	}
	if kept.ScannedAt == nil || !kept.ScannedAt.Equal(firstScannedAt) {
		t.Errorf("kept scanned_at = %v, want %v", kept.ScannedAt, firstScannedAt)
	}
	if !kept.Scanning {
		t.Errorf("kept scanning = false while restart scan (with 4s hanging codex) is running")
	}
	waitForSetupScan(t, base2, managementKey)

	// POST /setup/scan returns at once with scanning: true.
	code, scanBody := managedJSON(t, base2, managementKey, http.MethodPost, "/setup/scan", nil)
	if code != http.StatusOK {
		t.Fatalf("POST /setup/scan: %d %s", code, scanBody)
	}
	var scanning setupView
	if err := json.Unmarshal(scanBody, &scanning); err != nil || !scanning.Scanning {
		t.Fatalf("POST /setup/scan = %s (err=%v); want scanning: true", scanBody, err)
	}
	waitForSetupScan(t, base2, managementKey)

	// POST /setup/apply installs pluxy/claude from bundled registry even when another pick fails.
	code, applyBody := managedJSON(t, base2, managementKey, http.MethodPost, "/setup/apply", map[string]any{
		"picks": []string{"pluxy/claude", "nobody/missing"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /setup/apply: %d %s", code, applyBody)
	}
	var applyResp struct {
		Results []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(applyBody, &applyResp); err != nil || len(applyResp.Results) != 2 {
		t.Fatalf("apply reply = %s (%v)", applyBody, err)
	}
	if applyResp.Results[0].ID != "pluxy/claude" || applyResp.Results[0].Status != "done" {
		t.Errorf("result[0] = %+v", applyResp.Results[0])
	}
	if applyResp.Results[1].ID != "nobody/missing" || applyResp.Results[1].Status != "error" || applyResp.Results[1].Error == "" {
		t.Errorf("result[1] = %+v", applyResp.Results[1])
	}

	// GET /packages lists pluxy/claude.
	code, pkgBody := managedJSON(t, base2, managementKey, http.MethodGet, "/packages", nil)
	if code != http.StatusOK || !strings.Contains(string(pkgBody), `"pluxy/claude"`) {
		t.Errorf("GET /packages = %d %s; want pluxy/claude", code, pkgBody)
	}

	// POST /setup/done makes first_run false in GET /setup and GET /status.
	code, doneBody := managedJSON(t, base2, managementKey, http.MethodPost, "/setup/done", nil)
	if code != http.StatusOK || !strings.Contains(string(doneBody), `"first_run":false`) {
		t.Errorf("POST /setup/done = %d %s", code, doneBody)
	}
	_, setupAfter := managedJSON(t, base2, managementKey, http.MethodGet, "/setup", nil)
	var svDone setupView
	_ = json.Unmarshal(setupAfter, &svDone)
	if svDone.FirstRun {
		t.Errorf("GET /setup after done: first_run = true")
	}
	_, statusAfter := managedJSON(t, base2, managementKey, http.MethodGet, "/status", nil)
	var stDone struct {
		FirstRun bool `json:"first_run"`
	}
	_ = json.Unmarshal(statusAfter, &stDone)
	if stDone.FirstRun {
		t.Errorf("GET /status after done: first_run = true")
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
	env := isolatedSetupEnv(home, fakeBin)

	packOut := t.TempDir()
	runPack(t, buildPackTool(t), packOut, "-extensions", "claude,openai-compatible")
	bundledIndex := filepath.Join(packOut, "bundled", "index.json")

	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.json")
	cfgBytes, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	if err := os.WriteFile(cfgPath, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	base, stop := startCoreEnvStoppable(t, env, filepath.Join(binDir, "pluxy"), "serve", "-config", cfgPath, "-bundled", bundledIndex)
	defer stop()
	waitForSetupScan(t, base, managementKey)

	cliEnv := append(slicesClone(env),
		"PLUXY_URL="+base,
		"PLUXY_MANAGEMENT_KEY="+managementKey,
	)

	r := runCLI(t, cliEnv, "setup", "--json")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("pluxy setup --json: %s", r)
	}
	var sv setupView
	if err := json.Unmarshal([]byte(r.stdout), &sv); err != nil {
		t.Fatalf("pluxy setup --json invalid JSON: %v\n%s", err, r)
	}
	if len(sv.Picks) < 2 || sv.Picks[0].ID != "pluxy/claude" || !sv.Picks[0].Suggested {
		t.Errorf("pluxy setup --json picks = %+v", sv.Picks)
	}

	r = runCLI(t, cliEnv, "client", "show", "claude-code")
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("pluxy client show claude-code: %s", r)
	}
	if !strings.Contains(r.stdout, base) || !strings.Contains(r.stdout, "ANTHROPIC_BASE_URL") || !strings.Contains(r.stdout, "ANTHROPIC_AUTH_TOKEN") {
		t.Errorf("pluxy client show claude-code output missing config values:\n%s", r.stdout)
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

	// Apply and done through the CLI.
	r = runCLI(t, cliEnv, "setup", "apply", "--pick", "pluxy/claude", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"status":"done"`) {
		t.Errorf("pluxy setup apply --pick pluxy/claude --json: %s", r)
	}
	r = runCLI(t, cliEnv, "setup", "done", "--json")
	if r.code != 0 || !strings.Contains(r.stdout, `"first_run":false`) {
		t.Errorf("pluxy setup done --json: %s", r)
	}
}

func slicesClone(s []string) []string {
	out := make([]string, len(s))
	copy(out, s)
	return out
}
