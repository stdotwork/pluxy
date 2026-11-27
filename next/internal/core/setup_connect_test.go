package core

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/management"
)

const (
	connectURL = "http://127.0.0.1:8317"
	connectKey = "sk-test-client-key"
)

func claudeSnippet(t *testing.T) string {
	t.Helper()
	s, _ := ClientSnippetFor("claude-code", connectURL, connectKey)
	return s.Files[0].Content
}

func codexSnippet(t *testing.T) string {
	t.Helper()
	s, _ := ClientSnippetFor("codex", connectURL, connectKey)
	return s.Files[0].Content
}

// Claude Code's settings: Connect sets the four settings of the setup text, takes out an API key
// that would be sent instead, and keeps the rest of the file in its order; Disconnect gives the
// file back as it was, or, when the person changed it since, takes out only what Connect wrote.
func TestClaudeSettingsApplyAndClear(t *testing.T) {
	original := []byte(`{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "env": {
    "ANTHROPIC_API_KEY": "sk-ant-person",
    "FOO": "bar"
  },
  "model": "opus",
  "n": 1.50
}
`)
	applied, err := applyClaudeSettings(original, claudeSnippet(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, state := inspectFile(t, ".claude/settings.json", applied, inspectClaudeCode); state != stateConfigured {
		t.Errorf("after apply the state is %q, want configured:\n%s", state, applied)
	}
	root, err := parseJSONObj(applied)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range root {
		keys = append(keys, f.key)
	}
	if strings.Join(keys, ",") != "permissions,env,model,n" {
		t.Errorf("top-level order = %v, want the file's own", keys)
	}
	if v, _ := root.get("n"); string(v) != "1.50" {
		t.Errorf("n = %s, want 1.50 as written", v)
	}
	env, _, _ := root.sub("env")
	if _, ok := env.get("ANTHROPIC_API_KEY"); ok {
		t.Errorf("ANTHROPIC_API_KEY is still in env:\n%s", applied)
	}
	for k, want := range map[string]string{"FOO": `"bar"`, "ANTHROPIC_BASE_URL": `"` + connectURL + `"`, "ANTHROPIC_AUTH_TOKEN": `"` + connectKey + `"`} {
		if v, _ := env.get(k); string(v) != want {
			t.Errorf("env %s = %s, want %s", k, v, want)
		}
	}
	if claudeToken(applied) != connectKey {
		t.Errorf("claudeToken = %q", claudeToken(applied))
	}

	// Unchanged since: the original, byte for byte.
	back, err := clearClaudeSettings(original, applied, applied)
	if err != nil || string(back) != string(original) {
		t.Errorf("clear of an unchanged file = %q, %v; want the original", back, err)
	}

	// Changed since: the person's changes stay, Connect's settings go, the API key comes back.
	edited := strings.Replace(string(applied), `"model": "opus"`, `"model": "sonnet", "theme": "dark"`, 1)
	edited = strings.Replace(edited, `"FOO": "bar"`, `"FOO": "baz"`, 1)
	back, err = clearClaudeSettings(original, applied, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseJSONObj(back)
	if err != nil {
		t.Fatal(err)
	}
	genv, _, _ := got.sub("env")
	for k, want := range map[string]string{"FOO": `"baz"`, "ANTHROPIC_API_KEY": `"sk-ant-person"`} {
		if v, _ := genv.get(k); string(v) != want {
			t.Errorf("after clear env %s = %s, want %s:\n%s", k, v, want, back)
		}
	}
	for _, k := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY", "CLAUDE_CODE_AUTO_MODE_SERVER"} {
		if _, ok := genv.get(k); ok {
			t.Errorf("after clear env still has %s:\n%s", k, back)
		}
	}
	if v, _ := got.get("model"); string(v) != `"sonnet"` {
		t.Errorf("after clear model = %s, want the person's sonnet", v)
	}
	if v, _ := got.get("theme"); string(v) != `"dark"` {
		t.Errorf("after clear theme = %s, want the person's dark", v)
	}

	// No file before: apply makes one, clear of it unchanged removes it, and clear of it changed
	// keeps only what the person added.
	fresh, err := applyClaudeSettings(nil, claudeSnippet(t))
	if err != nil {
		t.Fatal(err)
	}
	if back, err := clearClaudeSettings(nil, fresh, fresh); err != nil || back != nil {
		t.Errorf("clear of a file Connect made = %q, %v; want no file", back, err)
	}
	added := strings.Replace(string(fresh), "{\n", "{\n  \"theme\": \"dark\",\n", 1)
	back, err = clearClaudeSettings(nil, fresh, []byte(added))
	if err != nil || string(back) != "{\n  \"theme\": \"dark\"\n}\n" {
		t.Errorf("clear of a made file the person added to = %q, %v", back, err)
	}
}

func TestClaudeSettingsRefused(t *testing.T) {
	for name, b := range map[string]string{
		"not an object":   `[1]`,
		"invalid":         `{"env": `,
		"text after":      `{} {}`,
		"key twice":       `{"model": "a", "model": "b"}`,
		"another schema":  `{"$schema": "https://example.test/schema.json"}`,
		"env not strings": `{"env": {"X": 1}}`,
		"env not object":  `{"env": "x"}`,
		"byte-order mark": utf8BOM + `{}`,
	} {
		if _, err := applyClaudeSettings([]byte(b), claudeSnippet(t)); err == nil {
			t.Errorf("%s: apply took %q", name, b)
		} else if _, ok := err.(*errNotOurs); !ok {
			t.Errorf("%s: error %T %v is not a refusal", name, err, err)
		}
	}
	for _, b := range []string{"", "  \n"} {
		if _, err := applyClaudeSettings([]byte(b), claudeSnippet(t)); err != nil {
			t.Errorf("apply refused an empty file %q: %v", b, err)
		}
	}
}

const codexOriginal = `# my codex
model = "gpt-6.1-sol" # the default
approval_policy = "on-request"

[projects."/home/me/x"]
trust_level = "trusted"

[mcp_servers.docs]
command = "npx"
args = [
  "-y",
  ["nested"]
]
prompt = """
[not a table]
key = "not a key"
"""
`

// Codex's config: Connect puts model_provider with the root keys, before the first table, and
// pluxy's provider in its table; every other line stays as written, values over several lines
// included. Disconnect gives the file back, or takes out only what Connect wrote.
func TestCodexConfigApplyAndClear(t *testing.T) {
	applied, err := applyCodexConfig([]byte(codexOriginal), codexSnippet(t))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(codexOriginal, `approval_policy = "on-request"`+"\n",
		`approval_policy = "on-request"`+"\n"+`model_provider = "pluxy"`+"\n", 1) +
		"\n[model_providers.pluxy]\nname = \"pluxy\"\nbase_url = \"" + connectURL + "/v1\"\nwire_api = \"responses\"\nexperimental_bearer_token = \"" + connectKey + "\"\n"
	if string(applied) != want {
		t.Errorf("apply =\n%s\nwant\n%s", applied, want)
	}
	if _, state := inspectFile(t, ".codex/config.toml", applied, inspectCodex); state != stateConfigured {
		t.Errorf("after apply the state is %q, want configured", state)
	}
	if codexToken(applied) != connectKey {
		t.Errorf("codexToken = %q", codexToken(applied))
	}
	if back, err := clearCodexConfig([]byte(codexOriginal), applied, applied); err != nil || string(back) != codexOriginal {
		t.Errorf("clear of an unchanged file = %q, %v; want the original", back, err)
	}
	// The person changes another table after: theirs stays, Connect's lines go.
	edited := strings.Replace(string(applied), `trust_level = "trusted"`, `trust_level = "untrusted"`, 1)
	back, err := clearCodexConfig([]byte(codexOriginal), applied, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(codexOriginal, `"trusted"`, `"untrusted"`, 1); string(back) != want {
		t.Errorf("clear of an edited file =\n%s\nwant\n%s", back, want)
	}

	// No file: made, and taken away again.
	fresh, err := applyCodexConfig(nil, codexSnippet(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(fresh) != codexSnippet(t)+"\n" {
		t.Errorf("apply on no file =\n%s", fresh)
	}
	if back, err := clearCodexConfig(nil, fresh, fresh); err != nil || back != nil {
		t.Errorf("clear of a made file = %q, %v; want no file", back, err)
	}
}

// A provider table already there: its keys change in place (a key's comment kept), the
// credentials that would win over the bearer token go, and Disconnect puts each back.
func TestCodexConfigExistingProvider(t *testing.T) {
	original := "model_provider = \"openai\" # mine\r\n\r\n[model_providers.pluxy]\r\nname = \"old\"\r\nbase_url = \"http://old.test/v1\"\r\nenv_key = \"PLUXY_KEY\"\r\nextra = 1\r\n\r\n[model_providers.pluxy.capabilities]\r\nremote_compaction = \"v2\"\r\n"
	applied, err := applyCodexConfig([]byte(original), codexSnippet(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "model_provider = \"pluxy\" # mine\r\n\r\n[model_providers.pluxy]\r\nname = \"pluxy\"\r\nbase_url = \"" + connectURL + "/v1\"\r\nextra = 1\r\nwire_api = \"responses\"\r\nexperimental_bearer_token = \"" + connectKey + "\"\r\n\r\n[model_providers.pluxy.capabilities]\r\nremote_compaction = \"v2\"\r\n"
	if string(applied) != want {
		t.Errorf("apply =\n%q\nwant\n%q", applied, want)
	}
	edited := strings.Replace(string(applied), "extra = 1", "extra = 2", 1)
	back, err := clearCodexConfig([]byte(original), applied, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	p := parseCodexConfigTOML(back)
	if p.modelProvider != "openai" || p.baseURL != "http://old.test/v1" || p.bearerToken != "" {
		t.Errorf("clear = %+v:\n%s", p, back)
	}
	for _, line := range []string{`model_provider = "openai" # mine`, `name = "old"`, `env_key = "PLUXY_KEY"`, "extra = 2", `remote_compaction = "v2"`} {
		if !strings.Contains(string(back), line+"\r\n") {
			t.Errorf("clear lost %q:\n%q", line, back)
		}
	}
	if strings.Contains(string(back), "wire_api") {
		t.Errorf("clear kept wire_api, which Connect added:\n%q", back)
	}
}

func TestCodexConfigRefused(t *testing.T) {
	for name, b := range map[string]string{
		"dotted provider keys":  "model_providers.pluxy.base_url = \"http://x/v1\"\n",
		"inline provider":       "[model_providers]\npluxy = { base_url = \"http://x/v1\" }\n",
		"provider dotted keys":  "[model_providers]\npluxy.base_url = \"http://x/v1\"\n",
		"another wire_api":      "[model_providers.pluxy]\nwire_api = \"chat\"\n",
		"provider twice":        "[model_providers.pluxy]\nname = \"a\"\n[model_providers.pluxy]\nname = \"b\"\n",
		"array of providers":    "[[model_providers.pluxy]]\nname = \"a\"\n",
		"a string that runs on": "x = \"abc\n",
		"a value that runs on":  "x = [1,\n",
		"not UTF-8":             "x = \"\xff\"\n",
	} {
		if _, err := applyCodexConfig([]byte(b), codexSnippet(t)); err == nil {
			t.Errorf("%s: apply took %q", name, b)
		} else if _, ok := err.(*errNotOurs); !ok {
			t.Errorf("%s: error %T %v is not a refusal", name, err, err)
		}
	}
}

// inspectFile writes b as rel in a new home and reads it back with inspect.
func inspectFile(t *testing.T, rel string, b []byte, inspect func(home, coreURL string) (string, string)) (string, string) {
	t.Helper()
	home := t.TempDir()
	p := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return inspect(home, connectURL)
}

// connectHome is a new home folder for core's user, for a test of connect and disconnect.
func connectHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func connectCall(t *testing.T, h http.Handler, verb, tool string) (int, management.ClientConnection, string) {
	t.Helper()
	rec := manageCall(t, h, http.MethodPost, "/setup/clients/"+tool+"/"+verb, "")
	var reply management.ClientConnection
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
			t.Fatalf("%s %s reply %s: %v", verb, tool, rec.Body, err)
		}
	}
	return rec.Code, reply, rec.Body.String()
}

func clientKeys(t *testing.T, h http.Handler) []management.ClientKey {
	t.Helper()
	rec := manageCall(t, h, http.MethodGet, "/client-keys", "")
	var r management.ClientKeysReply
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r.Keys
}

// Connect and Disconnect through the management API on real files: the file changed with a key
// made for the tool, a private record of the file as it was, the file given back as it was by
// Disconnect (its mode too) and the key made for it removed; a second connect keeps the key.
func TestConnectDisconnectClaudeCode(t *testing.T) {
	home := connectHome(t)
	h, cfgPath := startManagementCore(t, `{"listen":"127.0.0.1:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	dir := filepath.Join(home, ".claude")
	settings := filepath.Join(dir, "settings.json")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	original := "{\n  \"model\": \"opus\"\n}\n"
	if err := os.WriteFile(settings, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings, 0o644); err != nil {
		t.Fatal(err)
	}

	before := clientKeys(t, h)
	code, reply, body := connectCall(t, h, "connect", "claude-code")
	if code != http.StatusOK {
		t.Fatalf("connect = %d %s", code, body)
	}
	if !reply.KeyMade || reply.Key == nil || reply.Key.Label != "Claude Code" || reply.Client.State != stateConfigured || !reply.Client.ConnectedByPluxy || !reply.Client.CanConnect {
		t.Errorf("connect reply = %+v", reply)
	}
	if len(reply.Files) != 1 || reply.Files[0] != (management.ClientConnectionFile{Path: "~/.claude/settings.json", Change: management.ClientFileChanged}) {
		t.Errorf("connect files = %+v", reply.Files)
	}
	keys := clientKeys(t, h)
	if len(keys) != 2 {
		t.Fatalf("client keys after connect = %+v, want the old one and one for Claude Code", keys)
	}
	b, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	token := claudeToken(b)
	cfg, _ := os.ReadFile(cfgPath)
	if token == "" || !strings.Contains(string(cfg), token) || strings.Contains(body, token) {
		t.Errorf("the file's key %q is not a key in the config, or the reply shows it: %s", token, body)
	}
	if _, state := inspectClaudeCode(home, connectURL); state != stateConfigured {
		t.Errorf("settings after connect read %q", state)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(settings); fi.Mode().Perm() != 0o600 {
			t.Errorf("settings mode after connect = %v, want 0600: it holds a key", fi.Mode().Perm())
		}
	}
	recPath := filepath.Join(filepath.Dir(cfgPath), "state", "tools", "claude-code.json")
	if fi, err := os.Stat(recPath); err != nil {
		t.Fatalf("no record of the connect: %v", err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", fi.Mode().Perm())
	}

	// Again: the key the file holds is kept, nothing changes.
	code, again, body := connectCall(t, h, "connect", "claude-code")
	if code != http.StatusOK || again.KeyMade || again.Files[0].Change != management.ClientFileUnchanged || len(clientKeys(t, h)) != 2 {
		t.Errorf("connect again = %d %+v %s", code, again, body)
	}

	code, off, body := connectCall(t, h, "disconnect", "claude-code")
	if code != http.StatusOK || !off.KeyRemoved || off.Files[0].Change != management.ClientFileRestored || off.Client.ConnectedByPluxy || off.Client.State != stateNotConfigured {
		t.Errorf("disconnect = %d %+v %s", code, off, body)
	}
	if b, _ := os.ReadFile(settings); string(b) != original {
		t.Errorf("settings after disconnect = %q, want %q", b, original)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(settings); fi.Mode().Perm() != 0o644 {
			t.Errorf("settings mode after disconnect = %v, want 0644 as before", fi.Mode().Perm())
		}
	}
	if keys := clientKeys(t, h); len(keys) != 1 || keys[0].ID != before[0].ID {
		t.Errorf("client keys after disconnect = %+v, want the old one alone", keys)
	}
	if _, err := os.Stat(recPath); !os.IsNotExist(err) {
		t.Errorf("the record is still there after disconnect: %v", err)
	}
	if code, _, body := connectCall(t, h, "disconnect", "claude-code"); code != http.StatusConflict {
		t.Errorf("disconnect with nothing kept = %d %s, want 409", code, body)
	}
}

// Codex with no folder yet: Connect makes ~/.codex (0700) and the file (0600); Disconnect takes
// both away again.
func TestConnectDisconnectCodexNoFolder(t *testing.T) {
	home := connectHome(t)
	h, _ := startManagementCore(t, `{"listen":"127.0.0.1:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	// A label that is a key is refused, as POST /client-keys refuses it, before anything is written.
	if rec := manageCall(t, h, http.MethodPost, "/setup/clients/codex/connect", `{"label":"fake-client-key-1"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("connect with a key as the label = %d %s, want 400", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex")); !os.IsNotExist(err) {
		t.Errorf("a refused connect made ~/.codex: %v", err)
	}
	rec := manageCall(t, h, http.MethodPost, "/setup/clients/codex/connect", `{"label":"Codex"}`)
	var reply management.ClientConnection
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); rec.Code != http.StatusOK || err != nil ||
		reply.Files[0] != (management.ClientConnectionFile{Path: "~/.codex/config.toml", Change: management.ClientFileCreated}) ||
		reply.Key == nil || reply.Key.Label != "Codex" {
		t.Fatalf("connect = %d %+v %s", rec.Code, reply, rec.Body)
	}
	dir := filepath.Join(home, ".codex")
	if runtime.GOOS != "windows" {
		for p, want := range map[string]fs.FileMode{dir: 0o700, filepath.Join(dir, "config.toml"): 0o600} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
				t.Errorf("%s: %v %v, want mode %v", p, fi, err, want)
			}
		}
	}
	if _, state := inspectCodex(home, connectURL); state != stateConfigured {
		t.Errorf("config after connect reads %q", state)
	}
	code, off, body := connectCall(t, h, "disconnect", "codex")
	if code != http.StatusOK || off.Files[0].Change != management.ClientFileRemoved {
		t.Errorf("disconnect = %d %+v %s", code, off, body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("~/.codex is still there after disconnect: %v", err)
	}
}

// A link where the settings file is, or in place of its folder, is refused, and nothing changes:
// no key is made and the link's target stays as it was.
func TestConnectRefusesLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making links needs a privilege on Windows")
	}
	home := connectHome(t)
	h, _ := startManagementCore(t, `{"listen":"127.0.0.1:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	elsewhere := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(elsewhere, []byte("model = \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}
	for tool, why := range map[string]string{
		"codex":       "~/.codex/config.toml is a link",
		"claude-code": "~/.claude is a link",
	} {
		rec := manageCall(t, h, http.MethodPost, "/setup/clients/"+tool+"/connect", "")
		if rec.Code != http.StatusUnprocessableEntity || errorCodeOf(t, rec) != "refused" || !strings.Contains(rec.Body.String(), why) {
			t.Errorf("connect %s with a link = %d %s, want 422 saying %q", tool, rec.Code, rec.Body, why)
		}
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != "model = \"x\"\n" {
		t.Errorf("the link's target changed: %q", b)
	}
	if keys := clientKeys(t, h); len(keys) != 1 {
		t.Errorf("client keys after refused connects = %+v, want the one there was", keys)
	}
	rec := manageCall(t, h, http.MethodPost, "/setup/clients/opencode/connect", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("connect opencode = %d %s, want 404: core does not write its settings", rec.Code, rec.Body)
	}
}
