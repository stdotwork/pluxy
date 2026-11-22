package core

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/management"
)

// homeEnv is the environment that gives a program home as its home, config, data, state and
// cache folder.
func homeEnv(home string) map[string]string {
	xdg := filepath.Join(home, ".xdg")
	return map[string]string{
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(xdg, "config"),
		"XDG_DATA_HOME":   filepath.Join(xdg, "data"),
		"XDG_STATE_HOME":  filepath.Join(xdg, "state"),
		"XDG_CACHE_HOME":  filepath.Join(xdg, "cache"),
		"CODEX_HOME":      filepath.Join(home, ".codex"),
	}
}

func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for k, v := range homeEnv(home) {
		t.Setenv(k, v)
	}
	return home
}

// writeTestFile writes content to path, making its folders.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// treeOf lists every file and folder below root, with each file's contents.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[p] = "dir"
			return nil
		}
		b, err := os.ReadFile(p)
		out[p] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Each client tool's file reads with the state the desktop app's writer for it gives it.
func TestInspectClientStates(t *testing.T) {
	const coreURL = "http://127.0.0.1:8317"
	type step struct {
		name  string
		files map[string]string // path below home: contents ("" removes it)
		want  string
	}
	run := func(t *testing.T, inspect func(home, coreURL string) (string, string), steps []step) {
		home := setTestHome(t)
		for _, s := range steps {
			for p, c := range s.files {
				if c == "" {
					os.RemoveAll(filepath.Join(home, p))
					continue
				}
				writeTestFile(t, filepath.Join(home, p), c)
			}
			if _, got := inspect(home, coreURL); got != s.want {
				t.Errorf("%s: state %q, want %q", s.name, got, s.want)
			}
		}
	}
	const (
		nc  = management.ClientStateNotConfigured
		ok  = management.ClientStateConfigured
		nu  = management.ClientStateNeedsUpdate
		ref = management.ClientStateRefused
	)

	t.Run("claude-code", func(t *testing.T) {
		const f = ".claude/settings.json"
		env := func(base string) string {
			return `{"$schema": "https://json.schemastore.org/claude-code-settings.json", "env": {"ANTHROPIC_BASE_URL": "` + base +
				`", "ANTHROPIC_AUTH_TOKEN": "sk-client", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1", "CLAUDE_CODE_AUTO_MODE_SERVER": "0"}}`
		}
		run(t, inspectClaudeCode, []step{
			{"no file", nil, nc},
			{"empty file", map[string]string{f: " \n"}, nc},
			{"no base URL", map[string]string{f: `{"env": {"OTHER": "x"}}`}, nc},
			{"configured", map[string]string{f: env(coreURL)}, ok},
			{"localhost is another address", map[string]string{f: env("http://localhost:8317")}, nu},
			{"a final slash is another address", map[string]string{f: env(coreURL + "/")}, nu},
			{"another port", map[string]string{f: env("http://127.0.0.1:9999")}, nu},
			{"flags missing", map[string]string{f: `{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:8317", "ANTHROPIC_AUTH_TOKEN": "sk"}}`}, nu},
			{"another schema", map[string]string{f: `{"$schema": "https://example.com/s.json"}`}, ref},
			{"not JSON", map[string]string{f: `{"env": `}, ref},
			{"byte-order mark", map[string]string{f: "\xEF\xBB\xBF" + env(coreURL)}, ref},
			{"env not strings", map[string]string{f: `{"env": {"ANTHROPIC_BASE_URL": 1}}`}, ref},
			{"not an object", map[string]string{f: `[]`}, ref},
			{"null", map[string]string{f: `null`}, ref},
		})
	})

	t.Run("codex", func(t *testing.T) {
		const f = ".codex/config.toml"
		run(t, inspectCodex, []step{
			{"no file", nil, nc},
			{"no pluxy provider", map[string]string{f: "model = \"x\"\n"}, nc},
			{"configured without a model catalog", map[string]string{f: `# comment
model_provider = "pluxy"

[model_providers."pluxy"]
name = "pluxy"
base_url = "http://127.0.0.1:8317/v1" # trailing comment
wire_api = "responses"
experimental_bearer_token = "sk-client"
`}, ok},
			{"dotted keys", map[string]string{f: "model_provider = 'pluxy'\nmodel_providers.pluxy.base_url = \"http://127.0.0.1:8317/v1\"\nmodel_providers.pluxy.experimental_bearer_token = \"sk\"\n"}, ok},
			{"no /v1", map[string]string{f: "model_provider = \"pluxy\"\n[model_providers.pluxy]\nbase_url = \"http://127.0.0.1:8317\"\nexperimental_bearer_token = \"sk\"\n"}, nu},
			{"another provider in use", map[string]string{f: "model_provider = \"openai\"\n[model_providers.pluxy]\nbase_url = \"http://127.0.0.1:8317/v1\"\nexperimental_bearer_token = \"sk\"\n"}, nu},
			{"another wire API", map[string]string{f: "[model_providers.pluxy]\nwire_api = \"chat\"\n"}, ref},
			{"agents not a table", map[string]string{f: "agents = 1\n"}, ref},
			{"agents an array of tables", map[string]string{f: "[[agents]]\nenabled = true\n"}, ref},
			{"providers an inline table", map[string]string{f: "model_providers = { pluxy = { base_url = \"x\" } }\n"}, ref},
			{"not UTF-8", map[string]string{f: "model = \"\xff\"\n"}, ref},
		})
	})

	t.Run("opencode", func(t *testing.T) {
		const jsonc, json = ".config/opencode/opencode.jsonc", ".config/opencode/opencode.json"
		configured := `{
			// JSONC comment
			"$schema": "https://opencode.ai/config.json",
			"provider": {
				"pluxy": {
					"npm": "@ai-sdk/openai-compatible",
					"options": {"baseURL": "http://127.0.0.1:8317/v1", "apiKey": "sk-client",},
					"models": {"claude-opus-5-5": {"name": "Claude"},},
				},
			},
			"model": "pluxy/claude-opus-5-5",
		}`
		run(t, inspectOpenCode, []step{
			{"no file", nil, nc},
			{"opencode.json alone", map[string]string{json: configured}, ok},
			{"opencode.jsonc wins", map[string]string{jsonc: `{"model": "openai/gpt"}`}, nc},
			{"configured", map[string]string{jsonc: configured}, ok},
			{"model as an object", map[string]string{jsonc: strings.Replace(configured, `"pluxy/claude-opus-5-5"`, `{"providerID": "pluxy", "model": "claude-opus-5-5"}`, 1)}, ok},
			{"default model not listed", map[string]string{jsonc: strings.Replace(configured, `"pluxy/claude-opus-5-5"`, `"pluxy/other"`, 1)}, nu},
			{"another address", map[string]string{jsonc: strings.Replace(configured, "8317/v1", "8318/v1", 1)}, nu},
			{"OpenCode 2's providers.pluxy", map[string]string{jsonc: strings.Replace(configured, `"model":`, `"providers": {"pluxy": {}}, "model":`, 1)}, ref},
			{"byte-order mark", map[string]string{jsonc: "\xEF\xBB\xBF" + configured}, ref},
			{"a key twice", map[string]string{jsonc: `{"provider": {"pluxy": {"npm": "a", "npm": "b"}}}`}, ref},
			{"another schema", map[string]string{jsonc: `{"$schema": "https://example.com/s.json"}`}, ref},
			{"options not an object", map[string]string{jsonc: `{"provider": {"pluxy": {"options": []}}}`}, ref},
			{"a model not an object", map[string]string{jsonc: `{"provider": {"pluxy": {"models": {"m": 1}}}}`}, ref},
			{"model neither text nor an object", map[string]string{jsonc: `{"model": 1}`}, ref},
			{"not JSON", map[string]string{jsonc: `{"provider": `}, ref},
			{"unclosed block comment", map[string]string{jsonc: configured + "\n/* the rest"}, ref},
		})
	})

	t.Run("pi", func(t *testing.T) {
		const settings, cfg, marker = ".pi/agent/settings.json", ".pi/agent/cliproxyapi.json", ".pi/agent/pluxy-cliproxyapi-provider/.pluxy-adapter.json"
		const registered = `{"packages": ["./pluxy-cliproxyapi-provider"], "defaultProvider": "cliproxyapi", "defaultModel": "gpt-6.1-sol"}`
		run(t, inspectPi, []step{
			{"no files", nil, nc},
			{"adapter not listed", map[string]string{settings: `{"packages": ["npm:other"]}`}, nc},
			{"listed, no address", map[string]string{settings: registered}, nu},
			{"listed with an address, no adapter", map[string]string{cfg: `{"baseUrl": "http://127.0.0.1:8317", "apiKey": "sk"}`}, nu},
			{"configured", map[string]string{marker: `{"package": "pluxy/pi-setup", "version": "1"}`}, ok},
			{"/v1 is the same address", map[string]string{cfg: `{"baseUrl": "http://127.0.0.1:8317/v1/", "apiKey": "sk"}`}, ok},
			{"another provider ID", map[string]string{cfg: `{"baseUrl": "http://127.0.0.1:8317", "apiKey": "sk", "providerId": "mine"}`}, nu},
			{"marker of another package", map[string]string{cfg: `{"baseUrl": "http://127.0.0.1:8317", "apiKey": "sk"}`, marker: `{"package": "someone/else"}`}, nu},
			{"packages not a list", map[string]string{marker: `{"package": "pluxy/pi-setup"}`, settings: `{"packages": "x"}`}, ref},
			{"empty settings file", map[string]string{settings: " "}, ref},
			{"byte-order mark", map[string]string{settings: "\xEF\xBB\xBF" + registered}, ref},
			{"key not text", map[string]string{settings: registered, cfg: `{"apiKey": 1}`}, ref},
		})
	})

	t.Run("omp", func(t *testing.T) {
		const models, config = ".omp/agent/models.yaml", ".omp/agent/config.yaml"
		const provider = "providers:\n  pluxy:\n    baseUrl: http://127.0.0.1:8317/v1\n    apiKey: sk-client\n    api: openai-responses\n    discovery:\n      type: openai-models-list\n"
		run(t, inspectOMP, []step{
			{"no files", nil, nc},
			{"provider, no default role", map[string]string{models: provider}, nu},
			{"configured", map[string]string{config: "modelRoles:\n  default: pluxy/gpt-6.1-sol\n"}, ok},
			{"default role a list", map[string]string{config: "modelRoles:\n  default: [pluxy/a, pluxy/b]\n"}, nu},
			{"another address", map[string]string{config: "modelRoles:\n  default: pluxy/gpt-6.1-sol\n", models: strings.Replace(provider, "8317", "9999", 1)}, nu},
			{"two documents", map[string]string{models: provider + "---\na: 1\n"}, ref},
			{"provider not a mapping", map[string]string{models: "providers:\n  pluxy: text\n"}, ref},
			{"a key twice", map[string]string{models: "providers: {}\nproviders: {}\n"}, ref},
			{"default role a number", map[string]string{models: provider, config: "modelRoles:\n  default: 3\n"}, ref},
			{"top level a list", map[string]string{config: "- a\n"}, ref},
			{"not moved to YAML yet", map[string]string{models: "", config: "modelRoles:\n  default: pluxy/x\n", ".omp/agent/models.json": "{}"}, ref},
		})
	})
}

// What pluxy client show prints for each tool reads as configured by that tool's own reader.
func TestClientSnippetsReadAsConfigured(t *testing.T) {
	const coreURL = "http://127.0.0.1:8317"
	for _, tool := range ClientSnippetTools {
		t.Run(tool, func(t *testing.T) {
			home := setTestHome(t)
			snip, ok := ClientSnippetFor(tool, coreURL, "<client-key>")
			if !ok || len(snip.Files) == 0 {
				t.Fatalf("no snippet for %s", tool)
			}
			for _, f := range snip.Files {
				rest, ok := strings.CutPrefix(f.Path, "~/")
				if !ok {
					t.Fatalf("snippet path %q is not below ~", f.Path)
				}
				writeTestFile(t, filepath.Join(home, rest), f.Content)
			}
			if tool == "pi" {
				// What the snippet's note says Pi needs besides: the adapter the app installs.
				writeTestFile(t, filepath.Join(home, ".pi", "agent", piAdapterFolder, piAdapterMarker), `{"package": "pluxy/pi-setup"}`)
			}
			spec, ok := clientSpecByID(tool)
			if !ok {
				t.Fatalf("no client spec for %s", tool)
			}
			if _, state := spec.inspect(home, coreURL); state != management.ClientStateConfigured {
				t.Errorf("state %q, want configured; files: %+v", state, snip.Files)
			}
		})
	}
	if _, ok := ClientSnippetFor("pluxy/omp-setup", coreURL, "k"); !ok {
		t.Error("the setup package's ID names no tool")
	}
	if _, ok := ClientSnippetFor("cursor", coreURL, "k"); ok {
		t.Error("an unknown tool has a snippet")
	}
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

	// A Claude Code settings file its writer refuses: the provider is still suggested, the
	// pick that would write that file is not.
	picks = buildSetupPicks(regs, installed, management.SetupFound{
		Clients: []management.SetupClient{{ID: "claude-code", Installed: true, State: management.ClientStateRefused}},
	})
	if !picks[1].Suggested || picks[3].Suggested || picks[3].Because != "" {
		t.Errorf("refused settings: picks suggestions = %+v", picks)
	}
}

func TestProbeVersionKnownPathsAndFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell scripts")
	}
	home := setTestHome(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	scratch := t.TempDir()

	// Known install path for claude (~/.local/bin/claude) without being on PATH.
	localBin := filepath.Join(home, ".local", "bin")
	claudePath := filepath.Join(localBin, "claude")
	writeTestFile(t, claudePath, "#!/bin/sh\necho \"v2.5.0\"\n")
	os.Chmod(claudePath, 0o755)
	if got := findProgram("claude", clientSpecs[0].paths(home)); got != claudePath {
		t.Fatalf("findProgram = %q, want %q", got, claudePath)
	}
	if ver := probeVersion(context.Background(), claudePath, scratch, 2*time.Second); ver != "2.5.0" {
		t.Errorf("probeVersion = %q, want 2.5.0", ver)
	}

	// Non-zero exit code returns no version (e.g. a mise shim error).
	badPath := filepath.Join(localBin, "bad")
	writeTestFile(t, badPath, "#!/bin/sh\necho \"1.2.3\"\nexit 1\n")
	os.Chmod(badPath, 0o755)
	if ver := probeVersion(context.Background(), badPath, scratch, 2*time.Second); ver != "" {
		t.Errorf("non-zero exit probeVersion = %q, want empty", ver)
	}
}

// A probe that writes into its home, config, data, state, cache and temporary folders leaves the
// person's own folders as they were, and the scan leaves nothing in core's state folder.
func TestScanWritesNothingOutsideItsOwnFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell scripts")
	}
	home := setTestHome(t)
	t.Setenv("TMPDIR", filepath.Join(home, "tmp"))
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin")
	script := "#!/bin/sh\nfor d in \"$HOME\" \"$XDG_CONFIG_HOME\" \"$XDG_DATA_HOME\" \"$XDG_STATE_HOME\" \"$XDG_CACHE_HOME\" \"$CODEX_HOME\" \"$TMPDIR\" \"$PWD\"; do\n  mkdir -p \"$d/tool\" && echo written > \"$d/tool/file\"\ndone\necho 2.4.1\n"
	for _, name := range []string{"claude", "codex"} {
		writeTestFile(t, filepath.Join(bin, name), script)
		os.Chmod(filepath.Join(bin, name), 0o755)
	}
	for _, d := range homeEnv(home) {
		os.MkdirAll(d, 0o700)
	}
	os.MkdirAll(filepath.Join(home, "tmp"), 0o700)
	before := treeOf(t, home)
	state := filepath.Join(t.TempDir(), "state")

	found := scanMachine(context.Background(), state, "127.0.0.1:8317", false, setupProbeTimeout)
	for _, c := range found.Clients {
		if (c.ID == "claude-code" || c.ID == "codex") && (!c.Installed || c.Version != "2.4.1") {
			t.Errorf("%s: installed %v, version %q: the probe did not run", c.ID, c.Installed, c.Version)
		}
	}
	after := treeOf(t, home)
	for p := range after {
		if _, ok := before[p]; !ok {
			t.Errorf("the scan made %s", p)
		}
	}
	if left, _ := os.ReadDir(state); len(left) != 0 {
		t.Errorf("the scan left %v in the state folder", left)
	}
}

func TestSetupPersistenceAndApplyValidation(t *testing.T) {
	setTestHome(t)
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
	m2 := newSetupManager(filepath.Join(StateDir(cfgPath), setupFileName), c.setupCoreTarget, false)
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

// Core scans on its own only at the first start of a first run: once it listens, and never
// again once a scan is kept. A later start reads the kept scan's client states again against
// the address it listens on.
func TestSetupScansOnlyAtFirstStart(t *testing.T) {
	home := setTestHome(t)
	t.Setenv("PATH", t.TempDir())
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:1234", "ANTHROPIC_AUTH_TOKEN": "k", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1", "CLAUDE_CODE_AUTO_MODE_SERVER": "0"}}`)
	path := filepath.Join(t.TempDir(), "config.json")
	writeTestFile(t, path, safeConfig)
	claudeState := func(f management.SetupFound) string {
		for _, c := range f.Clients {
			if c.ID == "claude-code" {
				return c.State
			}
		}
		return ""
	}

	c, err := Start(Options{ConfigPath: path, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, scanning, at, _ := c.setup.snapshot(); scanning || at != nil {
		t.Fatalf("core scanned before it listened: scanning %v, scanned at %v", scanning, at)
	}
	c.SetListenAddr("127.0.0.1:1234")
	var kept time.Time
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		_, scanning, at, found := c.setup.snapshot()
		if !scanning && at != nil {
			kept = *at
			if s := claudeState(found); s != management.ClientStateConfigured {
				t.Errorf("claude-code state %q, want configured", s)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first start's scan did not finish")
		}
	}
	c.Stop()

	c, err = Start(Options{ConfigPath: path, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	c.SetListenAddr("127.0.0.1:5678")
	time.Sleep(200 * time.Millisecond)
	firstRun, scanning, at, found := c.setup.snapshot()
	if !firstRun || scanning || at == nil || !at.Equal(kept) {
		t.Errorf("second start: first run %v, scanning %v, scanned at %v; want the kept scan of %v", firstRun, scanning, at, kept)
	}
	if found.Core.Listen != "127.0.0.1:5678" || claudeState(found) != management.ClientStateNeedsUpdate {
		t.Errorf("second start: listen %q, claude-code %q; want 127.0.0.1:5678 and needs-update", found.Core.Listen, claudeState(found))
	}
}

// A kept scan's client states name core's address, scheme included: a start whose scheme is
// not the one they were read with reads them again, even though the address is the same.
func TestSetupRefreshesClientStatesForTLS(t *testing.T) {
	home := setTestHome(t)
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
		`{"env": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:1234", "ANTHROPIC_AUTH_TOKEN": "k", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1", "CLAUDE_CODE_AUTO_MODE_SERVER": "0"}}`)
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, setupFileName), `{"format": 0, "first_run": false, "scanned_at": "2026-11-20T00:00:00Z", "found": {"clients": [{"id": "claude-code", "name": "Claude Code", "installed": true, "config": "~/.claude/settings.json", "state": "`+management.ClientStateConfigured+`"}], "sign_ins": [], "imports": [], "core": {"listen": "127.0.0.1:1234", "reachable_from_network": false}}}`)
	target := func() (string, bool) { return "127.0.0.1:1234", true }
	m := newSetupManager(filepath.Join(dir, setupFileName), target, false)
	defer m.stop()
	m.listening("127.0.0.1:1234", true)
	_, _, _, found := m.snapshot()
	state := ""
	for _, c := range found.Clients {
		if c.ID == "claude-code" {
			state = c.State
		}
	}
	if state != management.ClientStateNeedsUpdate {
		t.Errorf("claude-code state %q after a tls-only change, want needs-update", state)
	}
}

// With no state/setup.json, a core that already has packages or accounts is not on its first
// run; once setup.json exists, it says.
func TestFirstRunOfAnUpgrade(t *testing.T) {
	setTestHome(t)
	start := func(dir string) *Core {
		t.Helper()
		c, err := Start(Options{ConfigPath: filepath.Join(dir, "config.json"), SafeMode: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(c.Stop)
		return c
	}
	fresh := t.TempDir()
	writeTestFile(t, filepath.Join(fresh, "config.json"), safeConfig)
	if !start(fresh).Status().FirstRun {
		t.Error("a new install is not on its first run")
	}

	upgrade := t.TempDir()
	writeTestFile(t, filepath.Join(upgrade, "config.json"), safeConfig)
	if err := packages.SaveState(filepath.Join(upgrade, "packages"), packages.State{
		"pluxy/claude": {ID: "pluxy/claude", Version: "0.1.0", Registry: "bundled", Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if start(upgrade).Status().FirstRun {
		t.Error("an install with packages and no setup.json is on its first run")
	}

	writeTestFile(t, filepath.Join(upgrade, "state", setupFileName), `{"format": 0, "first_run": true, "found": {}}`)
	if !start(upgrade).Status().FirstRun {
		t.Error("setup.json's first_run was not kept")
	}

	brokenOnly := t.TempDir()
	writeTestFile(t, filepath.Join(brokenOnly, "config.json"), safeConfig)
	writeTestFile(t, filepath.Join(brokenOnly, "credentials", "broken.json"), `{"id":`)
	if start(brokenOnly).Status().FirstRun {
		t.Error("an install whose only credential file is broken is on its first run")
	}

	m := newSetupManager(filepath.Join(t.TempDir(), setupFileName), nil, true)
	if m.isFirstRun() {
		t.Error("accounts or packages in use, no setup.json: first run")
	}
}

// No scan starts once stop has begun, so stop's wait never races a new scan's start.
func TestSetupScanAfterStop(t *testing.T) {
	setTestHome(t)
	t.Setenv("PATH", t.TempDir())
	target := func() (string, bool) { return "127.0.0.1:1", false }
	dir := t.TempDir()
	m := newSetupManager(filepath.Join(dir, setupFileName), target, false)
	m.stop()
	m.triggerScan()
	if _, scanning, _, _ := m.snapshot(); scanning {
		t.Error("a scan started after stop")
	}
	for range 20 {
		m := newSetupManager(filepath.Join(dir, setupFileName), target, false)
		var wg sync.WaitGroup
		wg.Go(m.triggerScan)
		wg.Go(m.stop)
		wg.Wait()
		m.triggerScan()
		m.wg.Wait()
	}
}

func TestSetupReportsRegistryCount(t *testing.T) {
	setTestHome(t)
	t.Setenv("PATH", t.TempDir())
	const cfg = `{"listen":"127.0.0.1:0","client_keys":["fake-client-key-1"],"management_key":"fake-mgmt-key"}`

	getSetup := func(t *testing.T, opts Options) map[string]any {
		t.Helper()
		c, err := Start(opts)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Stop()
		req := httptest.NewRequest(http.MethodGet, "/v0/management/setup", nil)
		req.Header.Set("Authorization", "Bearer fake-mgmt-key")
		req.RemoteAddr = "127.0.0.1:12345"
		rec := httptest.NewRecorder()
		c.Management().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /setup: status = %d (%s)", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}

	// No bundled registry and none in config: registries is 0.
	dir0 := t.TempDir()
	cfg0 := filepath.Join(dir0, "config.json")
	writeTestFile(t, cfg0, cfg)
	body0 := getSetup(t, Options{ConfigPath: cfg0, SafeMode: true})
	raw0, ok := body0["registries"]
	if !ok {
		t.Fatalf("GET /setup omitted registries field: %v", body0)
	}
	if raw0 != float64(0) {
		t.Errorf("registries with none = %v, want 0", raw0)
	}

	// With a bundled registry: registries is 1.
	dir1 := t.TempDir()
	cfg1 := filepath.Join(dir1, "config.json")
	writeTestFile(t, cfg1, cfg)
	bundled := filepath.Join(dir1, "bundled", "index.json")
	writeTestFile(t, bundled, `{"format":0,"packages":{}}`)
	body1 := getSetup(t, Options{ConfigPath: cfg1, SafeMode: true, Bundled: bundled})
	if body1["registries"] != float64(1) {
		t.Errorf("registries with bundled = %v, want 1", body1["registries"])
	}
}
