package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/examples/oauth/oauthtest"
	mgmt "github.com/stdotwork/pluxy/next/management"
)

// GET /setup/clients/{id}/paste against a real core, and pluxy client show through it.
func TestClientPaste(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	env := isolatedSetupEnv(home, t.TempDir())
	base, client, _ := startSetupCore(t, env, filepath.Join(t.TempDir(), "config.json"), "")
	ctx := context.Background()
	paste := "/setup/clients/%s/paste"

	// No reply holds a client key's value or the management key.
	secretFree := func(what string, body []byte) {
		t.Helper()
		for _, secret := range []string{managementKey, clientKey} {
			if strings.Contains(string(body), secret) {
				t.Errorf("%s holds a key's value: %s", what, body)
			}
		}
	}

	for _, tool := range []string{"claude-code", "codex", "opencode", "pi", "omp"} {
		status, body := managedJSON(t, base, managementKey, http.MethodGet, fmt.Sprintf(paste, tool), nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s: %d %s", tool, status, body)
		}
		secretFree(tool, body)
		var p mgmt.ClientPaste
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("%s: %v: %s", tool, err, body)
		}
		if p.Tool != tool || p.Name == "" || p.BaseURL != base || p.Key != "<client-key>" || len(p.Files) == 0 {
			t.Errorf("%s: reply = %+v", tool, p)
		}
		for _, f := range p.Files {
			if f.Path == "" || f.Content == "" {
				t.Errorf("%s: empty file %+v", tool, f)
			}
		}
		if !strings.Contains(p.Files[0].Content, base) || !strings.Contains(p.Files[0].Content, "<client-key>") {
			t.Errorf("%s: first file does not point at core with the placeholder:\n%s", tool, p.Files[0].Content)
		}
	}

	// Other names for a tool give its canonical ID.
	for name, want := range map[string]string{"oh-my-pi": "omp", "Codex-CLI": "codex", "pluxy/pi-setup": "pi"} {
		p, err := client.ClientPaste(ctx, name, "", "")
		if err != nil || p.Tool != want {
			t.Errorf("ClientPaste(%q) = %q, %v; want %q", name, p.Tool, err, want)
		}
	}

	// key_id: a key core holds names it by ID and masked form; one it does not hold is a 404.
	keys, err := client.ClientKeys(ctx)
	if err != nil || len(keys) == 0 {
		t.Fatalf("client keys: %v %v", keys, err)
	}
	status, body := managedJSON(t, base, managementKey, http.MethodGet, fmt.Sprintf(paste, "claude-code")+"?key_id="+keys[0].ID, nil)
	secretFree("key_id", body)
	var p mgmt.ClientPaste
	want := fmt.Sprintf("<client-key:%s (%s)>", keys[0].ID, keys[0].Masked)
	if err := json.Unmarshal(body, &p); status != http.StatusOK || err != nil || p.Key != want || !strings.Contains(p.Files[0].Content, want) {
		t.Errorf("key_id of a held key: %d, key %q, want %q: %s", status, p.Key, want, body)
	}
	var apiErr *mgmt.APIError
	if _, err := client.ClientPaste(ctx, "claude-code", "no-such-key", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
		t.Errorf("key_id of a key core does not hold: %v", err)
	}
	apiErr = nil
	if _, err := client.ClientPaste(ctx, "cursor", "", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || apiErr.Code != "not_found" {
		t.Errorf("unknown tool: %v", err)
	}

	// pluxy client show prints the reply's files, and with --json the reply body as it came.
	cliEnv := append(slices.Clone(env), "PLUXY_URL="+base, "PLUXY_MANAGEMENT_KEY="+managementKey)
	reply, err := client.ClientPaste(ctx, "pi", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := runCLI(t, cliEnv, "client", "show", "pi")
	if r.code != 0 || r.stderr != "Replace <client-key> with a client key: core's config file holds them, and \"pluxy key add\" makes another.\nReplace <model-id> with the name of a model your accounts serve: \"pluxy model list\" lists them.\n" || !strings.HasPrefix(r.stdout, "Client: "+reply.Name+"\nAddress: "+reply.BaseURL+"\n") || !strings.HasSuffix(r.stdout, "\n\n"+reply.Note+"\n") {
		t.Fatalf("pluxy client show pi: %s", r)
	}
	for _, f := range reply.Files {
		if !strings.Contains(r.stdout, "\n"+f.Path+":\n"+f.Content+"\n") {
			t.Errorf("pluxy client show pi does not print %s as the call returns it:\n%s", f.Path, r.stdout)
		}
	}
	status, raw := managedJSON(t, base, managementKey, http.MethodGet, fmt.Sprintf(paste, "pi"), nil)
	r = runCLI(t, cliEnv, "client", "show", "pi", "--json")
	if status != http.StatusOK || r.code != 0 || r.stderr != "" || r.stdout != string(raw) {
		t.Errorf("pluxy client show pi --json is not the call's body:\n%s\nbody: %s", r, raw)
	}
	r = runCLI(t, cliEnv, "client", "show", "pi", "--key-id", "no-such-key")
	if r.code != 5 {
		t.Errorf("pluxy client show pi --key-id no-such-key: %s", r)
	}
}

// A person whose accounts serve models of another service than the tool's own (here two models
// the tool has never heard of): the text for the tool names one of them, the models to choose
// from come with it, a request for the tool's own default is refused with those models named, and
// pluxy client show --model names the one asked for.
func TestClientPasteNamesAServedModel(t *testing.T) {
	t.Parallel()
	srv := oauthtest.New(t)
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "walk", "models": []string{"fake-gpt-1", "fake-gpt-mini"},
				"settings":   map[string]string{"base_url": srv.URL},
				"credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("text")},
			}},
		},
	})
	client := &mgmt.Client{URL: base, Key: managementKey}
	ctx := context.Background()

	p, err := client.ClientPaste(ctx, "claude-code", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Model != "fake-gpt-1" || !slices.Equal(p.Models, []string{"fake-gpt-1", "fake-gpt-mini"}) {
		t.Errorf("model %q of %q, want fake-gpt-1 of both", p.Model, p.Models)
	}
	text := p.Files[0].Content
	for _, want := range []string{`"model": "fake-gpt-1"`, `"ANTHROPIC_DEFAULT_OPUS_MODEL": "fake-gpt-1"`, `"ANTHROPIC_DEFAULT_HAIKU_MODEL": "fake-gpt-1"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the Claude Code settings lack %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<model-id>") {
		t.Errorf("the Claude Code settings still hold the placeholder:\n%s", text)
	}

	// The model the call names is the one in the text, and any name is taken: an account added
	// later may serve it.
	p, err = client.ClientPaste(ctx, "codex", "", "fake-gpt-mini")
	if err != nil || p.Model != "fake-gpt-mini" || !strings.Contains(p.Files[0].Content, `model = "fake-gpt-mini"`) {
		t.Errorf("codex with a model asked for: %q, %v: %+v", p.Model, err, p.Files)
	}
	var apiErr *mgmt.APIError
	if _, err := client.ClientPaste(ctx, "codex", "", "bad\nname"); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_request" {
		t.Errorf("a model name with a line break: %v", err)
	}

	// The tool's own default is not served: the refusal a tool shows names what is.
	resp := postPath(t, ctx, base, "/v1/chat/completions", clientKey,
		`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hi"}]}`)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var refused struct {
		Error struct{ Message string }
	}
	_ = json.Unmarshal(raw, &refused)
	const wantMessage = `no account serves model "claude-opus-5-5". pluxy serves: fake-gpt-1, fake-gpt-mini. Set the tool's model to one of them, or add a model alias to an account so that "claude-opus-5-5" goes to one of them.`
	if resp.StatusCode != http.StatusNotFound || refused.Error.Message != wantMessage {
		t.Errorf("a request for the tool's own default: %d %s", resp.StatusCode, raw)
	}

	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}
	r := runCLI(t, env, "client", "show", "codex", "--model", "fake-gpt-mini")
	if r.code != 0 || !strings.Contains(r.stdout, `model = "fake-gpt-mini"`) || strings.Contains(r.stderr, "<model-id>") {
		t.Errorf("pluxy client show codex --model: %s", r)
	}
	r = runCLI(t, env, "client", "show", "codex")
	if r.code != 0 || !strings.Contains(r.stdout, `model = "fake-gpt-1"`) {
		t.Errorf("pluxy client show codex names the best match: %s", r)
	}
}

func TestClientPasteAliasesPickToolModel(t *testing.T) {
	t.Parallel()
	srv := oauthtest.New(t)
	base := runCore(t, map[string]any{
		"listen":          "127.0.0.1:0",
		"client_keys":     []string{clientKey},
		"management_key":  managementKey,
		"credentials_dir": filepath.Join(t.TempDir(), "credentials"),
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, "oauth", "pluxy-extension.json"),
			"accounts": []any{map[string]any{
				"id": "walk", "models": []string{"alpha-chat", "claude-sonnet-5-5", "gpt-6.1-codex"},
				"settings":   map[string]string{"base_url": srv.URL},
				"credential": map[string]any{"kind": "oauth", "access_token": srv.IssueToken("text")},
			}},
		},
	})
	client := &mgmt.Client{URL: base, Key: managementKey}
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		wantTool  string
		wantModel string
	}{
		{"claude-code", "claude-code", "claude-sonnet-5-5"},
		{"claude", "claude-code", "claude-sonnet-5-5"},
		{"Claude-Code", "claude-code", "claude-sonnet-5-5"},
		{"pluxy/claude-code", "claude-code", "claude-sonnet-5-5"},
		{" claude ", "claude-code", "claude-sonnet-5-5"},
		{"codex", "codex", "gpt-6.1-codex"},
		{"codex-cli", "codex", "gpt-6.1-codex"},
		{"Codex-CLI", "codex", "gpt-6.1-codex"},
		{"pluxy/codex-setup", "codex", "gpt-6.1-codex"},
		{" codex-cli ", "codex", "gpt-6.1-codex"},
	} {
		p, err := client.ClientPaste(ctx, tc.name, "", "")
		if err != nil {
			t.Fatalf("ClientPaste(%q): %v", tc.name, err)
		}
		if p.Tool != tc.wantTool || p.Model != tc.wantModel {
			t.Errorf("ClientPaste(%q): tool=%q model=%q, want tool=%q model=%q", tc.name, p.Tool, p.Model, tc.wantTool, tc.wantModel)
		}
		if len(p.Files) == 0 || !strings.Contains(p.Files[0].Content, tc.wantModel) {
			t.Errorf("ClientPaste(%q): file does not name %q:\n%+v", tc.name, tc.wantModel, p.Files)
		}
	}

	for _, tc := range []struct {
		rawPath   string
		wantTool  string
		wantModel string
	}{
		{"/setup/clients/pluxy%2Fclaude-code/paste", "claude-code", "claude-sonnet-5-5"},
		{"/setup/clients/%20Claude-Code%20/paste", "claude-code", "claude-sonnet-5-5"},
		{"/setup/clients/pluxy%2Fcodex-setup/paste", "codex", "gpt-6.1-codex"},
	} {
		status, body := managedJSON(t, base, managementKey, http.MethodGet, tc.rawPath, nil)
		var p mgmt.ClientPaste
		if status != http.StatusOK || json.Unmarshal(body, &p) != nil || p.Tool != tc.wantTool || p.Model != tc.wantModel {
			t.Errorf("GET %s: status=%d tool=%q model=%q body=%s, want 200 tool=%q model=%q", tc.rawPath, status, p.Tool, p.Model, body, tc.wantTool, tc.wantModel)
		}
	}

	env := []string{"PLUXY_URL=" + base, "PLUXY_MANAGEMENT_KEY=" + managementKey}
	for _, tc := range []struct {
		arg  string
		want string
	}{
		{"claude", `"model": "claude-sonnet-5-5"`},
		{"Claude-Code", `"model": "claude-sonnet-5-5"`},
		{"pluxy/claude-code", `"model": "claude-sonnet-5-5"`},
		{" claude ", `"model": "claude-sonnet-5-5"`},
		{"codex-cli", `model = "gpt-6.1-codex"`},
		{"pluxy/codex-setup", `model = "gpt-6.1-codex"`},
	} {
		r := runCLI(t, env, "client", "show", tc.arg)
		if r.code != 0 || !strings.Contains(r.stdout, tc.want) {
			t.Errorf("pluxy client show %q: want %s in stdout, got %s", tc.arg, tc.want, r)
		}
	}
}
