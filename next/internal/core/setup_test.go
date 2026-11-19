package core

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/management"
)

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, ".xdg")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(xdg, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(xdg, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(xdg, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(xdg, "cache"))
	return home
}

func TestInspectClientStates(t *testing.T) {
	coreURL := "http://127.0.0.1:8317"

	t.Run("claude-code", func(t *testing.T) {
		home := setTestHome(t)
		if _, st := inspectClaudeCode(home, coreURL); st != management.ClientStateNotConfigured {
			t.Fatalf("missing file: got %q", st)
		}
		dir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "settings.json")
		// Configured (localhost matches loopback core).
		os.WriteFile(p, []byte(`{
			"$schema": "https://json.schemastore.org/claude-code-settings.json",
			"env": {
				"ANTHROPIC_BASE_URL": "http://localhost:8317",
				"ANTHROPIC_AUTH_TOKEN": "sk-client",
				"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
				"CLAUDE_CODE_AUTO_MODE_SERVER": "0"
			}
		}`), 0o600)
		if cfg, st := inspectClaudeCode(home, coreURL); cfg != "~/.claude/settings.json" || st != management.ClientStateConfigured {
			t.Errorf("configured: got (%q, %q)", cfg, st)
		}
		// Wrong port -> needs-update.
		os.WriteFile(p, []byte(`{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:9999", "ANTHROPIC_AUTH_TOKEN": "sk-client", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1", "CLAUDE_CODE_AUTO_MODE_SERVER": "0"}}`), 0o600)
		if _, st := inspectClaudeCode(home, coreURL); st != management.ClientStateNeedsUpdate {
			t.Errorf("wrong port: got %q", st)
		}
		// Missing required flags -> needs-update.
		os.WriteFile(p, []byte(`{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:8317", "ANTHROPIC_AUTH_TOKEN": "sk-client"}}`), 0o600)
		if _, st := inspectClaudeCode(home, coreURL); st != management.ClientStateNeedsUpdate {
			t.Errorf("missing flags: got %q", st)
		}
	})

	t.Run("codex", func(t *testing.T) {
		home := setTestHome(t)
		if _, st := inspectCodex(home, coreURL); st != management.ClientStateNotConfigured {
			t.Fatalf("missing file: got %q", st)
		}
		dir := filepath.Join(home, ".codex")
		os.MkdirAll(dir, 0o700)
		p := filepath.Join(dir, "config.toml")
		os.WriteFile(p, []byte(`# comment
model_provider = "pluxy"
model_catalog_json = "pluxy-model-catalog.json"

[model_providers."pluxy"]
name = "pluxy"
base_url = "http://127.0.0.1:8317/v1" # trailing comment
wire_api = "responses"
experimental_bearer_token = "sk-client"
`), 0o600)
		if cfg, st := inspectCodex(home, coreURL); cfg != "~/.codex/config.toml" || st != management.ClientStateConfigured {
			t.Errorf("configured: got (%q, %q)", cfg, st)
		}
		// Missing /v1 or wrong port -> needs-update.
		os.WriteFile(p, []byte("model_provider = \"pluxy\"\n[model_providers.pluxy]\nbase_url = \"http://127.0.0.1:8317\"\nexperimental_bearer_token = \"sk\"\n"), 0o600)
		if _, st := inspectCodex(home, coreURL); st != management.ClientStateNeedsUpdate {
			t.Errorf("missing catalog and /v1: got %q", st)
		}
	})

	t.Run("opencode", func(t *testing.T) {
		home := setTestHome(t)
		dir := filepath.Join(home, ".config", "opencode")
		os.MkdirAll(dir, 0o700)
		jsonPath := filepath.Join(dir, "opencode.json")
		os.WriteFile(jsonPath, []byte(`{
			// JSONC comment
			"provider": {
				"pluxy": {
					"npm": "@ai-sdk/openai-compatible",
					"options": {
						"baseURL": "http://127.0.0.1:8317/v1",
						"apiKey": "sk-client",
					},
					"models": {
						"claude-opus-5-5": {"name": "Claude"},
					},
				},
			},
			"model": "pluxy/claude-opus-5-5",
		}`), 0o600)
		if cfg, st := inspectOpenCode(home, coreURL); cfg != "~/.config/opencode/opencode.json" || st != management.ClientStateConfigured {
			t.Errorf("opencode.json configured: got (%q, %q)", cfg, st)
		}
		// When opencode.jsonc exists, it takes precedence.
		jsoncPath := filepath.Join(dir, "opencode.jsonc")
		os.WriteFile(jsoncPath, []byte(`{"providers":{"pluxy":{}},"provider":{"pluxy":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"http://127.0.0.1:8317/v1","apiKey":"sk"},"models":{"m":{}}}},"model":"pluxy/m"}`), 0o600)
		if cfg, st := inspectOpenCode(home, coreURL); cfg != "~/.config/opencode/opencode.jsonc" || st != management.ClientStateNeedsUpdate {
			t.Errorf("opencode.jsonc with v2 provider: got (%q, %q)", cfg, st)
		}
	})

	t.Run("pi", func(t *testing.T) {
		home := setTestHome(t)
		agentDir := filepath.Join(home, ".pi", "agent")
		os.MkdirAll(filepath.Join(agentDir, "pluxy-cliproxyapi-provider"), 0o700)
		os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{
			"packages": ["./pluxy-cliproxyapi-provider"],
			"defaultProvider": "cliproxyapi",
			"defaultModel": "gpt-6.1-sol"
		}`), 0o600)
		os.WriteFile(filepath.Join(agentDir, "cliproxyapi.json"), []byte(`{
			"baseUrl": "http://127.0.0.1:8317",
			"apiKey": "sk-client"
		}`), 0o600)
		os.WriteFile(filepath.Join(agentDir, "pluxy-cliproxyapi-provider", ".pluxy-adapter.json"), []byte(`{}`), 0o600)
		if cfg, st := inspectPi(home, coreURL); cfg != "~/.pi/agent/settings.json" || st != management.ClientStateConfigured {
			t.Errorf("pi configured: got (%q, %q)", cfg, st)
		}
		os.Remove(filepath.Join(agentDir, "pluxy-cliproxyapi-provider", ".pluxy-adapter.json"))
		if _, st := inspectPi(home, coreURL); st != management.ClientStateNeedsUpdate {
			t.Errorf("pi missing adapter marker: got %q", st)
		}
	})

	t.Run("omp", func(t *testing.T) {
		home := setTestHome(t)
		agentDir := filepath.Join(home, ".omp", "agent")
		os.MkdirAll(agentDir, 0o700)
		os.WriteFile(filepath.Join(agentDir, "models.yaml"), []byte(`
providers:
  pluxy:
    baseUrl: http://127.0.0.1:8317/v1
    apiKey: sk-client
    api: openai-responses
    discovery:
      type: openai-models-list
`), 0o600)
		os.WriteFile(filepath.Join(agentDir, "config.yaml"), []byte(`
modelRoles:
  default: pluxy/gpt-6.1-sol
`), 0o600)
		if cfg, st := inspectOMP(home, coreURL); cfg != "~/.omp/agent/models.yaml" || st != management.ClientStateConfigured {
			t.Errorf("omp configured: got (%q, %q)", cfg, st)
		}
		os.WriteFile(filepath.Join(agentDir, "models.yaml"), []byte("providers:\n  pluxy:\n    baseUrl: http://127.0.0.1:9999/v1\n"), 0o600)
		if _, st := inspectOMP(home, coreURL); st != management.ClientStateNeedsUpdate {
			t.Errorf("omp wrong url: got %q", st)
		}
	})
}

func TestScanSignInsAndImports(t *testing.T) {
	home := setTestHome(t)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	// Empty file must not count as signed in.
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(""), 0o600)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("not-even-read"), 0o600)

	signIns := scanSignIns(home)
	if len(signIns) != 1 || signIns[0].Provider != "pluxy/claude" {
		t.Errorf("signIns = %+v, want only pluxy/claude", signIns)
	}

	cpaDir := filepath.Join(home, ".cli-proxy-api")
	customAuth := filepath.Join(home, "custom-cpa-auth")
	os.MkdirAll(cpaDir, 0o700)
	os.MkdirAll(customAuth, 0o700)
	os.WriteFile(filepath.Join(cpaDir, "config.yaml"), []byte("auth-dir: ~/custom-cpa-auth\n"), 0o600)
	os.WriteFile(filepath.Join(customAuth, "acc1.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(customAuth, "acc2.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(customAuth, "ignore.txt"), []byte("x"), 0o600)

	imports := scanImports(home)
	if len(imports) != 1 || imports[0].AuthDir != "~/custom-cpa-auth" || imports[0].Accounts != 2 {
		t.Errorf("imports = %+v", imports)
	}
}

func TestSetupCoreReachableFromNetwork(t *testing.T) {
	for _, tc := range []struct {
		listen string
		want   bool
	}{
		{"127.0.0.1:8317", false},
		{"[::1]:8317", false},
		{"localhost:8317", false},
		{":8317", true},
		{"0.0.0.0:8317", true},
		{"[::]:8317", true},
		{"192.168.1.5:8317", true},
	} {
		if got := isReachableFromNetwork(tc.listen); got != tc.want {
			t.Errorf("isReachableFromNetwork(%q) = %v, want %v", tc.listen, got, tc.want)
		}
	}
}

func TestBuildSetupPicksOrderAndNothingFound(t *testing.T) {
	regs := []management.Registry{{
		Packages: []management.RegistryPackage{
			{ID: "pluxy/devin", Kind: "provider", Tier: "verified", Title: "Devin", Summary: "Devin"},
			{ID: "pluxy/kimi", Kind: "provider", Tier: "pick", Title: "Kimi", Summary: "Kimi"},
			{ID: "pluxy/omp-setup", Kind: "client", Tier: "pick", Title: "OMP", Summary: "OMP"},
			{ID: "pluxy/claude", Kind: "provider", Tier: "pick", Title: "Claude", Summary: "Claude"},
			{ID: "pluxy/codex", Kind: "provider", Tier: "pick", Title: "Codex", Summary: "Codex"},
			{ID: "pluxy/claude-code", Kind: "client", Tier: "pick", Title: "Claude Code", Summary: "Claude Code"},
		},
	}}
	installed := packages.State{"pluxy/claude": {ID: "pluxy/claude", Version: "0.1.0"}}

	// Nothing found -> ordered list, nothing suggested.
	picks := buildSetupPicks(regs, installed, management.SetupFound{})
	wantIDs := []string{"pluxy/codex", "pluxy/claude", "pluxy/kimi", "pluxy/claude-code", "pluxy/omp-setup"}
	if len(picks) != len(wantIDs) {
		t.Fatalf("got %d picks, want %d: %+v", len(picks), len(wantIDs), picks)
	}
	for i, id := range wantIDs {
		if picks[i].ID != id {
			t.Errorf("picks[%d].ID = %q, want %q", i, picks[i].ID, id)
		}
		if picks[i].Suggested || picks[i].Because != "" {
			t.Errorf("picks[%d] unexpectedly suggested: %+v", i, picks[i])
		}
		if got, want := picks[i].Installed, id == "pluxy/claude"; got != want {
			t.Errorf("picks[%d].Installed = %v, want %v", i, got, want)
		}
	}

	// Claude Code installed -> pluxy/claude and pluxy/claude-code suggested.
	picks = buildSetupPicks(regs, installed, management.SetupFound{
		Clients: []management.SetupClient{{ID: "claude-code", Installed: true}},
	})
	if !picks[1].Suggested || !picks[3].Suggested || picks[0].Suggested {
		t.Errorf("picks suggestions = %+v", picks)
	}
}

func TestProbeVersionTimeoutAndKnownPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell scripts")
	}
	home := setTestHome(t)
	t.Setenv("PATH", "/usr/bin:/bin")

	// Known install path for claude (~/.local/bin/claude) without being on PATH.
	localBin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(localBin, 0o755)
	claudePath := filepath.Join(localBin, "claude")
	os.WriteFile(claudePath, []byte("#!/bin/sh\necho \"v2.5.0\"\n"), 0o755)
	if got := findProgram("claude", clientSpecs[0].paths(home)); got != claudePath {
		t.Fatalf("findProgram = %q, want %q", got, claudePath)
	}
	if ver := probeVersion(context.Background(), claudePath, 2*time.Second); ver != "2.5.0" {
		t.Errorf("probeVersion = %q, want 2.5.0", ver)
	}

	// Non-zero exit code returns no version (e.g. a mise shim error).
	badPath := filepath.Join(localBin, "bad")
	os.WriteFile(badPath, []byte("#!/bin/sh\necho \"1.2.3\"\nexit 1\n"), 0o755)
	if ver := probeVersion(context.Background(), badPath, 2*time.Second); ver != "" {
		t.Errorf("non-zero exit probeVersion = %q, want empty", ver)
	}

	// Hanging probe is killed at timeout and returns no version.
	hangPath := filepath.Join(localBin, "hang")
	os.WriteFile(hangPath, []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	start := time.Now()
	ver := probeVersion(context.Background(), hangPath, 150*time.Millisecond)
	if ver != "" {
		t.Errorf("hanging probeVersion = %q, want empty", ver)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("hanging probe took %v, expected ~150ms", elapsed)
	}
}

func TestSetupPersistenceAndApplyValidation(t *testing.T) {
	home := setTestHome(t)
	_ = home
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Start(Options{ConfigPath: cfgPath, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()

	if !c.Status().FirstRun {
		t.Fatalf("initial FirstRun = false")
	}
	if _, err := c.DoneSetup(); err != nil {
		t.Fatal(err)
	}
	if c.Status().FirstRun {
		t.Fatalf("FirstRun after DoneSetup = true")
	}

	// Reload setupManager from state/setup.json and verify first_run: false persists.
	m2 := newSetupManager(filepath.Join(StateDir(cfgPath), setupFileName), c.setupCoreTarget)
	if m2.isFirstRun() {
		t.Errorf("reloaded firstRun = true, want false")
	}

	// POST /setup/apply rejects unknown fields and empty picks with 400.
	mgmt := c.Management()
	for _, body := range []string{`{"picks":[]}`, `{"picks":["pluxy/claude"],"extra":1}`, `{bad`} {
		req := httptest.NewRequest(http.MethodPost, "/v0/management/setup/apply", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer fake-mgmt-key")
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		mgmt.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /setup/apply %s: status = %d, want 400 (%s)", body, rec.Code, rec.Body.String())
		}
	}
}
