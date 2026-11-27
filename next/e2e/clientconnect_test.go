package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	mgmt "github.com/stdotwork/pluxy/next/management"
)

// POST /setup/clients/{id}/connect and .../disconnect against a real core whose user's home folder
// is a temporary one: Claude Code through the management client, Codex through pluxy client
// connect and disconnect. Each tool's file points it at core with a key core takes, made for the
// tool; disconnect gives back the file as it was (or takes away the file and folder connect made)
// and the key goes. No reply, command output or log line holds a key's value.
func TestClientConnect(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := isolatedSetupEnv(home, t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	cfg, _ := json.Marshal(map[string]any{
		"listen":         "127.0.0.1:0",
		"client_keys":    []string{clientKey},
		"management_key": managementKey,
	})
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	base, logs, _, err := startCoreEnv(t, env, filepath.Join(binDir, "pluxy"), "serve", "-config", cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &mgmt.Client{URL: base, Key: managementKey}
	ctx := context.Background()

	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"env\": {\n    \"ANTHROPIC_API_KEY\": \"sk-ant-made-up\"\n  },\n  \"theme\": \"dark\"\n}\n"
	if err := os.WriteFile(settings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	accepts := func(key string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	var made []string
	secretFree := func(what, text string) {
		t.Helper()
		for _, k := range append([]string{managementKey, clientKey}, made...) {
			if strings.Contains(text, k) {
				t.Errorf("%s holds a key's value:\n%s", what, text)
			}
		}
	}

	// Claude Code, through the client.
	conn, err := client.ConnectClient(ctx, "claude-code", "")
	if err != nil {
		t.Fatalf("connect claude-code: %v", err)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Env   map[string]string `json:"env"`
		Theme string            `json:"theme"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatalf("settings after connect: %v\n%s", err, b)
	}
	key := file.Env["ANTHROPIC_AUTH_TOKEN"]
	made = append(made, key)
	if file.Env["ANTHROPIC_BASE_URL"] != base || key == "" || file.Theme != "dark" {
		t.Errorf("settings after connect:\n%s", b)
	}
	if _, ok := file.Env["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("settings still send the API key:\n%s", b)
	}
	if code := accepts(key); code != http.StatusOK {
		t.Errorf("core answers the key connect wrote with %d", code)
	}
	if !conn.KeyMade || conn.Key == nil || conn.Key.Label != "Claude Code" || conn.Client.State != mgmt.ClientStateConfigured || !conn.Client.ConnectedByPluxy {
		t.Errorf("connect reply = %+v", conn)
	}
	raw, _ := json.Marshal(conn)
	secretFree("the connect reply", string(raw))

	// GET /setup says so too.
	s, err := client.Setup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(s.Found.Clients, func(c mgmt.SetupClient) bool { return c.ID == "claude-code" })
	if i < 0 || s.Found.Clients[i].State != mgmt.ClientStateConfigured || !s.Found.Clients[i].ConnectedByPluxy || !s.Found.Clients[i].CanConnect {
		t.Errorf("GET /setup's claude-code after connect: %+v", s.Found.Clients)
	}

	// Codex, through the command: no ~/.codex yet.
	cliEnv := append(slices.Clone(env), "PLUXY_URL="+base, "PLUXY_MANAGEMENT_KEY="+managementKey)
	r := runCLI(t, cliEnv, "client", "connect", "codex")
	if r.code != 0 || !strings.Contains(r.stdout, "~/.codex/config.toml: created\n") || !strings.Contains(r.stdout, "Connected Codex CLI.") ||
		!strings.Contains(r.stdout, "pluxy client disconnect codex puts it back.") {
		t.Fatalf("pluxy client connect codex: %s", r)
	}
	toml, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var codexKey string
	for _, line := range strings.Split(string(toml), "\n") {
		if v, ok := strings.CutPrefix(line, "experimental_bearer_token = "); ok {
			codexKey = strings.Trim(v, `"`)
		}
	}
	made = append(made, codexKey)
	if !strings.HasPrefix(string(toml), "model_provider = \"pluxy\"\n") || !strings.Contains(string(toml), "base_url = \""+base+"/v1\"\n") || codexKey == "" || codexKey == key {
		t.Errorf("config.toml after connect:\n%s", toml)
	}
	if code := accepts(codexKey); code != http.StatusOK {
		t.Errorf("core answers Codex's key with %d", code)
	}
	secretFree("pluxy client connect codex", r.stdout+r.stderr)
	keys, err := client.ClientKeys(ctx)
	if err != nil || len(keys) != 3 {
		t.Errorf("client keys after two connects: %+v %v", keys, err)
	}

	// A tool core does not connect, and a disconnect of one it did not connect.
	var apiErr *mgmt.APIError
	if _, err := client.ConnectClient(ctx, "opencode", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("connect opencode: %v", err)
	}

	// Disconnect: Codex through the command, Claude Code through the client.
	r = runCLI(t, cliEnv, "client", "disconnect", "codex")
	if r.code != 0 || !strings.Contains(r.stdout, "~/.codex/config.toml: removed\n") || !strings.Contains(r.stdout, "The client key pluxy made for it is removed.") {
		t.Errorf("pluxy client disconnect codex: %s", r)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Errorf("~/.codex after disconnect: %v", err)
	}
	off, err := client.DisconnectClient(ctx, "claude-code")
	if err != nil {
		t.Fatalf("disconnect claude-code: %v", err)
	}
	if b, _ := os.ReadFile(settings); string(b) != original {
		t.Errorf("settings after disconnect = %q, want them as they were", b)
	}
	if !off.KeyRemoved || off.Client.ConnectedByPluxy || off.Client.State != mgmt.ClientStateNotConfigured ||
		len(off.Files) != 1 || off.Files[0].Change != mgmt.ClientFileRestored {
		t.Errorf("disconnect reply = %+v", off)
	}
	for _, k := range made {
		if code := accepts(k); code != http.StatusUnauthorized {
			t.Errorf("a key connect made is answered with %d after disconnect, want 401", code)
		}
	}
	if code := accepts(clientKey); code != http.StatusOK {
		t.Errorf("the key core started with is answered with %d", code)
	}
	if _, err := client.DisconnectClient(ctx, "claude-code"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("disconnect again: %v", err)
	}
	logs.waitFor(t, "setup: disconnected Claude Code")
	for _, k := range made {
		if logs.contains(k) {
			t.Errorf("core's log holds a key connect made")
		}
	}
}
