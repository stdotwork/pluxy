package core

import (
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/management"
)

const (
	connectURL = "http://127.0.0.1:8317"
	connectKey = "sk-test-client-key"
)

func claudeSnippet(t *testing.T) string {
	t.Helper()
	s, _ := clientSnippet("claude-code", connectURL, connectKey, "")
	return s.Files[0].Content
}

func codexSnippet(t *testing.T) string {
	t.Helper()
	s, _ := clientSnippet("codex", connectURL, connectKey, "")
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
// Disconnect (its mode too) and the key made for it removed; a second connect keeps the key and
// makes the file its owner's alone again.
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

	// Again, on the file made readable by others since: the key the file holds is kept, the bytes
	// do not change, and the file is its owner's alone again.
	if err := os.Chmod(settings, 0o644); err != nil {
		t.Fatal(err)
	}
	code, again, body := connectCall(t, h, "connect", "claude-code")
	if code != http.StatusOK || again.KeyMade || again.Files[0].Change != management.ClientFileUnchanged || len(clientKeys(t, h)) != 2 {
		t.Errorf("connect again = %d %+v %s", code, again, body)
	}
	if after, _ := os.ReadFile(settings); string(after) != string(b) {
		t.Errorf("settings changed on connect again:\n%s", after)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(settings); fi.Mode().Perm() != 0o600 {
			t.Errorf("settings mode after connect again on a 0644 file = %v, want 0600: it holds a key", fi.Mode().Perm())
		}
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

// Codex with no folder yet: Connect makes ~/.codex (0700) and the file (0600), a connect again
// makes the file 0600 again when it was opened up since; Disconnect takes both away again.
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
	// Connect again on the file made readable by others: unchanged, and its owner's alone again.
	toml := filepath.Join(dir, "config.toml")
	if err := os.Chmod(toml, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, again, body := connectCall(t, h, "connect", "codex"); code != http.StatusOK || again.KeyMade || again.Files[0].Change != management.ClientFileUnchanged {
		t.Errorf("connect again = %d %+v %s", code, again, body)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(toml); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("config.toml mode after connect again on a 0644 file = %v (%v), want 0600: it holds a key", fi.Mode().Perm(), err)
		}
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

// A settings file Connect cannot change (one it would have to guess at) is refused after the key
// for the tool is made: the key goes again, and the file, the config's keys and the records stay as
// they were.
func TestConnectRefusedFileLeavesNoKey(t *testing.T) {
	home := connectHome(t)
	h, cfgPath := startManagementCore(t, `{"listen":"127.0.0.1:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	files := map[string]string{
		"claude-code": filepath.Join(home, ".claude", "settings.json"),
		"codex":       filepath.Join(home, ".codex", "config.toml"),
	}
	refused := map[string]string{
		"claude-code": `{"env": `,
		"codex":       "[model_providers]\npluxy = { base_url = \"http://x/v1\" }\n",
	}
	cfgBefore, _ := os.ReadFile(cfgPath)
	for tool, p := range files {
		writeTestFile(t, p, refused[tool])
		rec := manageCall(t, h, http.MethodPost, "/setup/clients/"+tool+"/connect", "")
		if rec.Code != http.StatusUnprocessableEntity || errorCodeOf(t, rec) != "refused" {
			t.Errorf("connect %s on a file it cannot change = %d %s, want 422 refused", tool, rec.Code, rec.Body)
		}
		if b, _ := os.ReadFile(p); string(b) != refused[tool] {
			t.Errorf("%s's file changed on a refused connect: %q", tool, b)
		}
		if keys := clientKeys(t, h); len(keys) != 1 {
			t.Errorf("client keys after a refused connect of %s = %+v, want the one there was", tool, keys)
		}
	}
	if cfg, _ := os.ReadFile(cfgPath); !strings.Contains(string(cfg), "fake-client-key-1") || strings.Count(string(cfg), "sk-") != strings.Count(string(cfgBefore), "sk-") {
		t.Errorf("config after refused connects:\n%s", cfg)
	}
	if entries, err := os.ReadDir(filepath.Join(filepath.Dir(cfgPath), "state", "tools")); err == nil && len(entries) > 0 {
		t.Errorf("records after refused connects: %v", entries)
	}
}

// ownAddr is an address of one of this machine's interfaces other than loopback, for the address
// a browser on another machine reaches core by; the test skips without one.
func ownAddr(t *testing.T) string {
	t.Helper()
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	t.Skip("this machine has no address but loopback")
	return ""
}

// A tool's address matches core's when it reaches that core: for a core on every interface, a
// loopback address or another address of this machine of the listener's family, or of either
// family when the listener takes both, and localhost only then; for a core on one address, that
// address alone; for a core whose address is not known, no address.
func TestAtCore(t *testing.T) {
	own := ownAddr(t)
	own4 := netip.MustParseAddr(own).Unmap().Is4()
	ownURL := "http://" + net.JoinHostPort(own, "8317")
	every4, every6, one := coreAddress("0.0.0.0:8317", false), coreAddress("[::]:8317", false), coreAddress("10.0.0.2:8317", false)
	type match struct {
		address, core string
		want          bool
	}
	check := func(t *testing.T, cases []match) {
		t.Helper()
		for _, c := range cases {
			if got := atCore(c.address, c.core); got != c.want {
				t.Errorf("atCore(%q, %q) = %v, want %v", c.address, c.core, got, c.want)
			}
		}
	}
	dualStack := func(t *testing.T, both bool) {
		was := wildcardDualStack
		wildcardDualStack = func() bool { return both }
		t.Cleanup(func() { wildcardDualStack = was })
	}

	// Whether the listener takes both families or not.
	for _, both := range []bool{false, true} {
		dualStack(t, both)
		check(t, []match{
			{"http://127.0.0.1:8317", every4, true},
			{"http://127.0.0.2:8317", every4, true},
			{"http://[::1]:8317", every6, true},
			{ownURL, every4, own4 || both},
			{ownURL, every6, !own4 || both},
			{ownURL, coreAddress(":8317", false), own4 || both},
			{"http://198.51.100.9:8317", every4, false}, // not this machine
			{"http://[2001:db8::9]:8317", every6, false},
			{"http://127.0.0.1:8318", every4, false},
			{"https://127.0.0.1:8317", every4, false},
			{"http://127.0.0.1:8317/", every4, false},
			{"http://127.0.0.1", every4, false},
			{"http://10.0.0.2:8317", one, true},
			{"http://127.0.0.1:8317", one, false}, // core is not on loopback
			{"http://localhost:8317", coreAddress("127.0.0.1:8317", false), false},
			{"https://127.0.0.1:8317", coreAddress("127.0.0.1:8317", true), true},
			{"http://anything", "", false},
			{"http://127.0.0.1:8317", "", false},
		})
	}
	// A listener of one family: another server can hold the same port in the other family.
	dualStack(t, false)
	check(t, []match{
		{"http://[::1]:8317", every4, false},
		{"http://[::1]:8317", coreAddress(":8317", false), false},
		{"http://127.0.0.1:8317", every6, false},
		{"http://localhost:8317", every4, false}, // may resolve to ::1
		{"http://localhost:8317", every6, false},
	})
	// A listener that takes both.
	dualStack(t, true)
	check(t, []match{
		{"http://[::1]:8317", every4, true},
		{"http://127.0.0.1:8317", every6, true},
		{"http://localhost:8317", every4, true},
		{"http://localhost:8317", every6, true},
		{"http://[::ffff:127.0.0.1]:8317", every4, true},
	})
	if !atCoreV1("http://127.0.0.1:8317/v1", every4) || atCoreV1("http://127.0.0.1:8317", every4) {
		t.Error("atCoreV1 does not want /v1 after core's address")
	}
	if atCoreV1("http://127.0.0.1:8317/v1", "") {
		t.Error("atCoreV1 matches an address when core's is not known")
	}
}

// wildcardDualStack says what a listener on every interface opened as core opens its own does:
// a connection to ::1 reaches one on 0.0.0.0 exactly when it says the listener takes both.
func TestWildcardDualStack(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	c, err := net.DialTimeout("tcp", net.JoinHostPort("::1", port), 5*time.Second)
	if c != nil {
		c.Close()
	}
	if reached := err == nil; wildcardDualStack() != reached {
		t.Errorf("wildcardDualStack() = %v, but a connection to [::1]:%s reached a listener on 0.0.0.0: %v (%v)", wildcardDualStack(), port, reached, err)
	}
}

// With listen 0.0.0.0, every tool's settings that name the address the panel was reached by read
// as connected, as do the ones Connect writes (core's loopback address).
func TestConnectedAtEveryInterface(t *testing.T) {
	own := ownAddr(t)
	if !netip.MustParseAddr(own).Unmap().Is4() && !wildcardDualStack() {
		t.Skipf("this machine's address %s is IPv6, which a listener on 0.0.0.0 does not take here", own)
	}
	core := coreAddress("0.0.0.0:8317", false)
	reached := "http://" + net.JoinHostPort(own, "8317")
	for _, tool := range ClientSnippetTools {
		snip, _ := ClientSnippetFor(tool, reached, connectKey, "")
		home := t.TempDir()
		for _, f := range snip.Files {
			writeTestFile(t, filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(f.Path, "~/"))), f.Content)
		}
		if tool == "pi" {
			writeTestFile(t, filepath.Join(home, ".pi", "agent", piAdapterFolder, piAdapterMarker), `{"package": "pluxy/pi-setup"}`)
		}
		spec, _ := clientSpecByID(tool)
		if _, state := spec.inspect(home, core); state != stateConfigured {
			t.Errorf("%s at %s reads %q against %s", tool, reached, state, core)
		}
	}

	home := connectHome(t)
	h, _ := startManagementCore(t, `{"listen":"0.0.0.0:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)
	code, reply, body := connectCall(t, h, "connect", "claude-code")
	if code != http.StatusOK || reply.Client.State != stateConfigured {
		t.Fatalf("connect with listen 0.0.0.0 = %d %s", code, body)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); !strings.Contains(string(b), `"ANTHROPIC_BASE_URL": "http://127.0.0.1:8317"`) {
		t.Errorf("connect with listen 0.0.0.0 wrote:\n%s", b)
	}
}

// Claude Code with a model: apply sets model and the three ANTHROPIC_DEFAULT_*_MODEL variables;
// clear restores the person's own model (and any default model variable they had) or takes them
// out when they had none, while keeping changes the person made since.
func TestClaudeSettingsApplyAndClearWithModel(t *testing.T) {
	snip, _ := ClientSnippetFor("claude-code", connectURL, connectKey, "claude-sonnet-5-5")
	original := []byte(`{
  "env": {
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "my-opus",
    "FOO": "bar"
  },
  "model": "opus"
}
`)
	applied, err := applyClaudeSettings(original, snip.Files[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseJSONObj(applied)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := root.get("model"); string(v) != `"claude-sonnet-5-5"` {
		t.Errorf("applied model = %s, want claude-sonnet-5-5:\n%s", v, applied)
	}
	env, _, _ := root.sub("env")
	for _, k := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		if v, _ := env.get(k); string(v) != `"claude-sonnet-5-5"` {
			t.Errorf("applied env %s = %s, want claude-sonnet-5-5:\n%s", k, v, applied)
		}
	}
	// Edited elsewhere: Disconnect gives back the person's own model and opus default, and takes out
	// sonnet and haiku defaults that Connect added.
	edited := strings.Replace(string(applied), `"FOO": "bar"`, `"FOO": "baz"`, 1)
	back, err := clearClaudeSettings(original, applied, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseJSONObj(back)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.get("model"); string(v) != `"opus"` {
		t.Errorf("after clear model = %s, want opus:\n%s", v, back)
	}
	genv, _, _ := got.sub("env")
	if v, _ := genv.get("ANTHROPIC_DEFAULT_OPUS_MODEL"); string(v) != `"my-opus"` {
		t.Errorf("after clear ANTHROPIC_DEFAULT_OPUS_MODEL = %s, want my-opus:\n%s", v, back)
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
		if _, ok := genv.get(k); ok {
			t.Errorf("after clear env still has %s:\n%s", k, back)
		}
	}
	// No model before (and no file before): Connect adds them, edited Disconnect takes them out.
	fresh, err := applyClaudeSettings(nil, snip.Files[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	added := strings.Replace(string(fresh), "{\n", "{\n  \"theme\": \"dark\",\n", 1)
	back, err = clearClaudeSettings(nil, fresh, []byte(added))
	if err != nil || string(back) != "{\n  \"theme\": \"dark\"\n}\n" {
		t.Errorf("clear of a made file with a model = %q, %v; want only theme", back, err)
	}
	// Person changed model after Connect: Disconnect keeps the person's new model.
	changedModel := strings.Replace(string(applied), `"model": "claude-sonnet-5-5"`, `"model": "my-custom"`, 1)
	back, err = clearClaudeSettings(original, applied, []byte(changedModel))
	if err != nil {
		t.Fatal(err)
	}
	got, err = parseJSONObj(back)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.get("model"); string(v) != `"my-custom"` {
		t.Errorf("after clear with user-changed model = %s, want my-custom:\n%s", v, back)
	}
}

// Codex with a model: apply puts root model above tables (keeping an existing model's comment),
// passes the post-write check, and clear gives back the person's own model or takes it out when
// they had none.
func TestCodexConfigApplyAndClearWithModel(t *testing.T) {
	snip, _ := ClientSnippetFor("codex", connectURL, connectKey, "gpt-6.1-codex")
	applied, err := applyCodexConfig([]byte(codexOriginal), snip.Files[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(applied), `model = "gpt-6.1-codex" # the default`+"\n") {
		t.Errorf("apply did not update root model keeping comment:\n%s", applied)
	}
	if _, state := inspectFile(t, ".codex/config.toml", applied, inspectCodex); state != stateConfigured {
		t.Errorf("after apply with model state = %q, want configured", state)
	}
	// Edited elsewhere: Disconnect gives back the person's own model (with comment).
	edited := strings.Replace(string(applied), `trust_level = "trusted"`, `trust_level = "untrusted"`, 1)
	back, err := clearCodexConfig([]byte(codexOriginal), applied, []byte(edited))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(codexOriginal, `"trusted"`, `"untrusted"`, 1); string(back) != want {
		t.Errorf("clear of an edited file with model =\n%s\nwant\n%s", back, want)
	}
	// No root model before: Connect puts model above the first table, edited Disconnect takes it out.
	noModel := "[projects.\"/home/me/x\"]\ntrust_level = \"trusted\"\n"
	appliedNoModel, err := applyCodexConfig([]byte(noModel), snip.Files[0].Content)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(appliedNoModel), "[projects.")
	if !strings.Contains(head, `model_provider = "pluxy"`) || !strings.Contains(head, `model = "gpt-6.1-codex"`) {
		t.Errorf("root keys not above first table:\n%s", appliedNoModel)
	}
	editedNoModel := strings.Replace(string(appliedNoModel), `"trusted"`, `"untrusted"`, 1)
	back, err = clearCodexConfig([]byte(noModel), appliedNoModel, []byte(editedNoModel))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(noModel, `"trusted"`, `"untrusted"`, 1); string(back) != want {
		t.Errorf("clear when person had no model =\n%s\nwant\n%s", back, want)
	}
	// Person changed model after Connect: Disconnect keeps the person's new model.
	changedModel := strings.Replace(string(applied), `model = "gpt-6.1-codex"`, `model = "my-custom"`, 1)
	back, err = clearCodexConfig([]byte(codexOriginal), applied, []byte(changedModel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back), `model = "my-custom" # the default`) {
		t.Errorf("clear dropped user's changed model:\n%s", back)
	}
}

// Connect writes the best-matching served model for Claude Code and Codex (and an explicit model
// when asked, refusing an invalid one with 400); Disconnect puts back the person's own model (or
// takes it out when they had none).
func TestConnectWritesServedModelAndExplicitModel(t *testing.T) {
	home := connectHome(t)
	h, _ := startManagementCore(t, `{"listen":"127.0.0.1:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key","extensions":{"pluxy/fake":{"accounts":[{"id":"acc-1","models":["fake-gpt-1","claude-sonnet-5-5","gpt-6.1-codex"],"credential":{"kind":"api_key","api_key":"fake-0"}}]}}}`)

	// Invalid model in connect body is refused with 400 invalid_request before writing anything.
	rec := manageCall(t, h, http.MethodPost, "/setup/clients/claude-code/connect", `{"model":"bad\nname"}`)
	if rec.Code != http.StatusBadRequest || errorCodeOf(t, rec) != "invalid_request" {
		t.Fatalf("connect with bad model = %d %s, want 400 invalid_request", rec.Code, rec.Body)
	}
	if len(clientKeys(t, h)) != 1 {
		t.Fatalf("bad model left a client key behind")
	}

	// Claude Code with an existing model: Connect writes claude-sonnet-5-5; edited Disconnect restores opus.
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeTestFile(t, claudePath, "{\n  \"model\": \"opus\"\n}\n")
	code, reply, body := connectCall(t, h, "connect", "claude-code")
	if code != http.StatusOK || reply.Model != "claude-sonnet-5-5" {
		t.Fatalf("connect claude-code = %d model %q %s", code, reply.Model, body)
	}
	b, _ := os.ReadFile(claudePath)
	for _, want := range []string{`"model": "claude-sonnet-5-5"`, `"ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-sonnet-5-5"`, `"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-5-5"`, `"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-sonnet-5-5"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("claude settings missing %s:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), ModelPlaceholder) {
		t.Errorf("claude settings contain placeholder:\n%s", b)
	}
	// Edit another key, then Disconnect: opus comes back and ANTHROPIC_DEFAULT_* go.
	edited := strings.Replace(string(b), "{\n", "{\n  \"theme\": \"dark\",\n", 1)
	if err := os.WriteFile(claudePath, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, body := connectCall(t, h, "disconnect", "claude-code"); code != http.StatusOK {
		t.Fatalf("disconnect claude-code = %d %s", code, body)
	}
	after, _ := os.ReadFile(claudePath)
	if !strings.Contains(string(after), `"model": "opus"`) || strings.Contains(string(after), "ANTHROPIC_DEFAULT_") {
		t.Errorf("claude settings after edited disconnect did not restore opus:\n%s", after)
	}

	// Codex with an existing model: Connect writes gpt-6.1-codex; edited Disconnect restores gpt-6.1-sol.
	codexPath := filepath.Join(home, ".codex", "config.toml")
	writeTestFile(t, codexPath, codexOriginal)
	code, reply, body = connectCall(t, h, "connect", "codex")
	if code != http.StatusOK || reply.Model != "gpt-6.1-codex" {
		t.Fatalf("connect codex = %d model %q %s", code, reply.Model, body)
	}
	b, _ = os.ReadFile(codexPath)
	if !strings.Contains(string(b), `model = "gpt-6.1-codex" # the default`) {
		t.Errorf("codex config missing served model:\n%s", b)
	}
	if strings.Contains(string(b), ModelPlaceholder) {
		t.Errorf("codex config contains placeholder:\n%s", b)
	}
	editedCodex := strings.Replace(string(b), `trust_level = "trusted"`, `trust_level = "untrusted"`, 1)
	if err := os.WriteFile(codexPath, []byte(editedCodex), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, body := connectCall(t, h, "disconnect", "codex"); code != http.StatusOK {
		t.Fatalf("disconnect codex = %d %s", code, body)
	}
	after, _ = os.ReadFile(codexPath)
	if !strings.Contains(string(after), `model = "gpt-6.1-sol" # the default`) {
		t.Errorf("codex config after edited disconnect did not restore model:\n%s", after)
	}

	// Explicit model in POST body overrides the best match.
	rec = manageCall(t, h, http.MethodPost, "/setup/clients/codex/connect", `{"model":"fake-gpt-1"}`)
	var explicitReply management.ClientConnection
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &explicitReply) != nil || explicitReply.Model != "fake-gpt-1" {
		t.Fatalf("connect codex with explicit model = %d %+v %s", rec.Code, explicitReply, rec.Body)
	}
	b, _ = os.ReadFile(codexPath)
	if !strings.Contains(string(b), `model = "fake-gpt-1"`) {
		t.Errorf("codex config with explicit model wrote:\n%s", b)
	}
}

// When pluxy serves no model, Connect succeeds, writes no placeholder, keeps whatever model the
// file already had, and adds no model setting when the file had none.
func TestConnectWithNoServedModelsWritesNoPlaceholder(t *testing.T) {
	home := connectHome(t)
	h, _ := startManagementCore(t, `{"listen":"127.0.0.1:8317","client_keys":["fake-client-key-1"],"management_key":"mgmt-key"}`)

	// Existing files with their own models: kept untouched, no placeholder.
	claudePath := filepath.Join(home, ".claude", "settings.json")
	writeTestFile(t, claudePath, "{\n  \"model\": \"opus\"\n}\n")
	if code, reply, body := connectCall(t, h, "connect", "claude-code"); code != http.StatusOK || reply.Model != "" {
		t.Fatalf("connect claude-code with no served models = %d model %q %s", code, reply.Model, body)
	}
	cb, _ := os.ReadFile(claudePath)
	if strings.Contains(string(cb), ModelPlaceholder) || !strings.Contains(string(cb), `"model": "opus"`) || strings.Contains(string(cb), "ANTHROPIC_DEFAULT_") {
		t.Errorf("claude settings with no served models:\n%s", cb)
	}

	codexPath := filepath.Join(home, ".codex", "config.toml")
	writeTestFile(t, codexPath, codexOriginal)
	if code, reply, body := connectCall(t, h, "connect", "codex"); code != http.StatusOK || reply.Model != "" {
		t.Fatalf("connect codex with no served models = %d model %q %s", code, reply.Model, body)
	}
	tb, _ := os.ReadFile(codexPath)
	if strings.Contains(string(tb), ModelPlaceholder) || !strings.Contains(string(tb), `model = "gpt-6.1-sol" # the default`) {
		t.Errorf("codex config with no served models:\n%s", tb)
	}

	// Disconnect both, remove the files, and Connect with no file: no placeholder, no model key.
	_, _, _ = connectCall(t, h, "disconnect", "claude-code")
	_, _, _ = connectCall(t, h, "disconnect", "codex")
	_ = os.Remove(claudePath)
	_ = os.Remove(codexPath)

	if code, _, body := connectCall(t, h, "connect", "claude-code"); code != http.StatusOK {
		t.Fatalf("connect claude-code fresh with no served models = %d %s", code, body)
	}
	cb, _ = os.ReadFile(claudePath)
	if strings.Contains(string(cb), ModelPlaceholder) || strings.Contains(string(cb), `"model"`) || strings.Contains(string(cb), "ANTHROPIC_DEFAULT_") {
		t.Errorf("fresh claude settings with no served models wrote a model:\n%s", cb)
	}

	if code, _, body := connectCall(t, h, "connect", "codex"); code != http.StatusOK {
		t.Fatalf("connect codex fresh with no served models = %d %s", code, body)
	}
	tb, _ = os.ReadFile(codexPath)
	if strings.Contains(string(tb), ModelPlaceholder) || strings.Contains(string(tb), "\nmodel = ") || strings.HasPrefix(string(tb), "model = ") {
		t.Errorf("fresh codex config with no served models wrote a model:\n%s", tb)
	}
}
