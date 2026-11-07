package legacy

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// example is today's core's config.example.yaml (copied from core/), with its commented
// provider key examples uncommented when withKeys is set.
func example(t *testing.T, withKeys bool) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !withKeys {
		return b
	}
	lines := strings.Split(string(b), "\n")
	in := false
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "# BEGIN API KEY EXAMPLES"):
			in = true
		case strings.HasPrefix(l, "# END API KEY EXAMPLES"):
			in = false
		case in && strings.HasPrefix(l, "# "):
			lines[i] = l[2:]
		case in && l == "#":
			lines[i] = ""
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func translate(t *testing.T, yaml string) *Result {
	t.Helper()
	r, err := Translate([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// asJSON returns v as the JSON config would hold it, for comparing.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func equalJSON(t *testing.T, got any, want string) {
	t.Helper()
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if g := asJSON(t, got); !reflect.DeepEqual(g, w) {
		gb, _ := json.MarshalIndent(g, "", "  ")
		t.Fatalf("got\n%s\nwant\n%s", gb, want)
	}
}

func notices(r *Result) string { return strings.Join(r.Notices, "\n") }

func TestExampleConfig(t *testing.T) {
	r, err := Translate(example(t, false))
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, r.Config, `{
		"listen": ":8317",
		"client_keys": ["your-api-key-1", "your-api-key-2", "your-api-key-3"],
		"routing": {"max_wait_ms": 30000}
	}`)
	if r.AuthDir != "~/.cli-proxy-api" {
		t.Errorf("AuthDir = %q", r.AuthDir)
	}
	if r.Import != DefaultImportOptions {
		t.Errorf("Import = %+v", r.Import)
	}
	// The example sets request-retry: 3; everything else it sets is supported, off or at
	// today's default.
	if len(r.Notices) != 1 || !strings.Contains(r.Notices[0], "routing.retry.request-retry") {
		t.Errorf("notices:\n%s", notices(r))
	}
}

func TestExampleProviderSections(t *testing.T) {
	r, err := Translate(example(t, true))
	if err != nil {
		t.Fatal(err)
	}
	exts := asJSON(t, r.Config["extensions"]).(map[string]any)
	ids := map[string][]string{}
	for ext, x := range exts {
		for _, a := range x.(map[string]any)["accounts"].([]any) {
			ids[ext] = append(ids[ext], a.(map[string]any)["id"].(string))
		}
	}
	want := map[string][]string{
		Codex:            {"codex-1"},
		Claude:           {"claude-1", "claude-2"},
		Gemini:           {"gemini-1-1", "gemini-1-2", "gemini-2", "vertex-1"},
		XAI:              {"xai-1"},
		OpenAICompatible: {"openrouter-1", "openrouter-2"},
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("account IDs = %v, want %v", ids, want)
	}
	vertex := exts[Gemini].(map[string]any)["accounts"].([]any)[3]
	equalJSON(t, vertex, `{
		"id": "vertex-1", "weight": 5,
		"models": ["gemini-2.5-flash", "gemini-2.5-pro"],
		"settings": {"base_url": "https://example.com/api", "vertex": {}},
		"credential": {"kind": "api_key", "api_key": "vk-123..."}
	}`)
	// A key's null fields inherit the group's; its own weight and proxy are its own.
	second := exts[Gemini].(map[string]any)["accounts"].([]any)[1]
	equalJSON(t, second, `{
		"id": "gemini-1-2", "priority": 0, "weight": 2,
		"models": ["gemini-2.5-flash"],
		"settings": {"base_url": "https://generativelanguage.googleapis.com"},
		"credential": {"kind": "api_key", "api_key": "replace-with-second-gemini-key"}
	}`)
	n := notices(r)
	for _, want := range []string{
		"api-keys.codex.prefix", "api-keys.claude.models.alias", "api-keys.gemini.excluded-models",
		"api-keys.xai.websockets", "api-keys.meta", "api-keys.interactions",
		"api-keys.claude.cloak (making other clients look like Claude Code is out of scope)",
	} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
	if strings.Contains(n, "unknown") || strings.Contains(n, "experimental-cch-signing") {
		t.Errorf("notices name an unknown or dead key:\n%s", n)
	}
}

func TestOlderSpellings(t *testing.T) {
	r := translate(t, `
host: 127.0.0.1
port: 9000
api-keys: ["client-a"]
remote-management:
  allow-remote: true
  secret-key: "manage-me"
auth-dir: ./auths
request-retry: 0
max-retry-credentials: 2
routing:
  strategy: weighted-round-robin
  session-affinity: true
  session-affinity-ttl: 10m
disable-image-generation: chat
codex:
  disable-codex-cloaking: true
codex-api-key:
  - api-key: "codex-key"
    base-url: "https://codex.example.com"
    websockets: true
    weight: 3
    priority: 2
claude-api-key:
  - api-key: "claude-key"
    disable-codex-cloaking: true
openai-compatibility:
  - name: "local model"
    base-url: "http://127.0.0.1:1234/v1"
    api-key-entries:
      - api-key: "compat-1"
      - api-key: "compat-2"
        weight: 0
    models:
      - name: "qwen"
  - name: "off"
    disabled: true
    base-url: "http://127.0.0.1:1/v1"
    api-key-entries:
      - api-key: "compat-off"
`)
	equalJSON(t, r.Config, `{
		"listen": "127.0.0.1:9000",
		"client_keys": ["client-a"],
		"management_key": "manage-me",
		"routing": {"strategy": "weighted", "max_accounts": 2, "session_affinity": {"ttl_ms": 600000}},
		"extensions": {
			"pluxy/codex": {"accounts": [{
				"id": "codex-api-key-1", "priority": 2, "weight": 3,
				"settings": {"base_url": "https://codex.example.com", "websockets": true, "image_generation_tool": false},
				"credential": {"kind": "api_key", "api_key": "codex-key"}
			}]},
			"pluxy/claude": {"accounts": [{
				"id": "claude-api-key-1",
				"credential": {"kind": "api_key", "api_key": "claude-key"}
			}]},
			"pluxy/openai-compatible": {"accounts": [
				{"id": "local-model-1", "models": ["qwen"], "settings": {"base_url": "http://127.0.0.1:1234/v1"},
				 "credential": {"kind": "api_key", "api_key": "compat-1"}},
				{"id": "local-model-2", "weight": 0, "models": ["qwen"], "settings": {"base_url": "http://127.0.0.1:1234/v1"},
				 "credential": {"kind": "api_key", "api_key": "compat-2"}}
			]}
		}
	}`)
	if r.AuthDir != "./auths" {
		t.Errorf("AuthDir = %q", r.AuthDir)
	}
	if r.Import != (ImportOptions{CodexIdentity: false, ImageGenerationTool: false}) {
		t.Errorf("Import = %+v", r.Import)
	}
	// disable-codex-cloaking means nothing on a Claude key: it is not a Claude key field.
	if n := notices(r); !strings.Contains(n, "unknown keys, ignored: api-keys.claude.disable-codex-cloaking") || strings.Contains(n, "allow-remote") {
		t.Errorf("notices:\n%s", n)
	}
}

func TestV8SpellingWins(t *testing.T) {
	r := translate(t, `
port: 9000
server: {port: 9001}
remote-management: {secret-key: old, allow-remote: true}
management: {secret-key: new}
max-retry-interval: 7
routing: {retry: {max-retry-interval: 0}}
codex-api-key:
  - api-key: old-key
api-keys:
  codex:
    - name: c
      keys: [{api-key: new-key}]
`)
	equalJSON(t, r.Config, `{
		"listen": ":9001",
		"management_key": "new",
		"extensions": {"pluxy/codex": {"accounts": [{"id": "c", "credential": {"kind": "api_key", "api_key": "new-key"}}]}}
	}`)
	// allow-remote came from the older spelling and still counts: the v8 value wins only
	// field by field.
	if strings.Contains(notices(r), "allow-remote") {
		t.Errorf("notices:\n%s", notices(r))
	}
}

func TestNotices(t *testing.T) {
	r := translate(t, `
management: {secret-key: k}
server:
  tls: {enable: true, cert: c.pem, key: k.pem}
  discovery: {enabled: false, service-name: x}
  commercial-mode: true
  discovery-typo: 1
quota-exceeded: {switch-project: true}
routing:
  session-affinity-subagents: true
  session-affinity-rebind-on: quota
  cooldown: {disable-cooling: true}
plugins: {enabled: true, dir: plugins}
upstream:
  codex: {websocket-compression: false, model-level-cooling: true}
  claude: {tool-search: true, model-level-cooling: true, session-date-pin-days: 3}
observability:
  logs: {debug: false, error-logs-max-files: 10, request-log: true}
`)
	n := notices(r)
	for _, want := range []string{
		"not supported yet, ignored: observability.logs, routing.cooldown.disable-cooling, routing.session-affinity-rebind-on, server.tls, upstream.claude.tool-search\n",
		"dropped, ignored: plugins (replaced by packages), server.commercial-mode (it turns off features this core does not have), upstream.claude.session-date-pin-days (making other clients look like Claude Code is out of scope)\n",
		"unknown keys, ignored: server.discovery-typo\n",
		"management.allow-remote is not supported yet",
		"websocket-compression: false is not supported",
	} {
		if !strings.Contains(n+"\n", want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
	for _, quiet := range []string{"switch-project", "discovery (", "session-affinity-subagents", "error-logs-max-files", "debug", "model-level-cooling"} {
		if strings.Contains(n, quiet) {
			t.Errorf("notices name %q:\n%s", quiet, n)
		}
	}
}

func TestHashedManagementKey(t *testing.T) {
	r := translate(t, `remote-management: {secret-key: "$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234"}`)
	if _, ok := r.Config["management_key"]; ok {
		t.Errorf("a bcrypt hash became the management key: %v", r.Config)
	}
	if !strings.Contains(notices(r), "bcrypt hash") {
		t.Errorf("notices:\n%s", notices(r))
	}
}

func TestStrategyNames(t *testing.T) {
	for in, want := range map[string]any{
		"round-robin": nil, "fillfirst": "fill-first", "ff": "fill-first", "wrr": "weighted",
		"weighted": "weighted", "least-active": "least-active", "nonsense": nil,
	} {
		r := translate(t, "routing: {strategy: "+in+"}")
		got := asJSON(t, r.Config["routing"])
		var s any
		if m, ok := got.(map[string]any); ok {
			s = m["strategy"]
		}
		if s != want {
			t.Errorf("strategy %q became %v, want %v", in, s, want)
		}
	}
	if r := translate(t, "routing: {strategy: nonsense}"); !strings.Contains(notices(r), `"nonsense" is not known`) {
		t.Errorf("notices:\n%s", notices(r))
	}
}

func TestNewKeysPassThrough(t *testing.T) {
	r := translate(t, `
credentials_dir: creds
drop_policy: reject
packages: {dir: pk}
extensions:
  pluxy/codex: {idle_stop_ms: 0, accounts: [{id: own, credential: {kind: api_key, api_key: k}}]}
codex-api-key: [{api-key: from-yaml}]
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"max_wait_ms": 30000},
		"credentials_dir": "creds", "drop_policy": "reject", "packages": {"dir": "pk"},
		"extensions": {"pluxy/codex": {"idle_stop_ms": 0, "accounts": [
			{"id": "own", "credential": {"kind": "api_key", "api_key": "k"}},
			{"id": "codex-api-key-1", "credential": {"kind": "api_key", "api_key": "from-yaml"}}
		]}}
	}`)
}

func TestAccountIDsAreValidAndUnique(t *testing.T) {
	r := translate(t, `
api-keys:
  claude:
    - name: "my key/1"
      keys: [{api-key: a}]
    - name: "my key/1"
      keys: [{api-key: b}]
    - name: "..."
      keys: [{api-key: c}]
`)
	var got []string
	for _, a := range asJSON(t, r.Config["extensions"]).(map[string]any)[Claude].(map[string]any)["accounts"].([]any) {
		got = append(got, a.(map[string]any)["id"].(string))
	}
	if want := []string{"my-key-1", "my-key-1-2", "account"}; !reflect.DeepEqual(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
}

func TestRefusedFiles(t *testing.T) {
	for _, y := range []string{
		"- a list",
		"config-version: 7",
		"port: x",
		"api-keys: 3",
		"codex-api-key: {api-key: k}",
		"access: [client-secret]\nclaude-api-key: [{api-key: k}]",
		"remote-management: secret\nclaude-api-key: [{api-key: k}]",
		"codex-api-key: [{api-key: k, base-url: [https://x]}]",
		"codex-api-key: [{api-key: k, websockets: yes-please}]",
		"routing: {session-affinity: sometimes}",
		"disable-image-generation: sometimes",
		"api-keys: {codex: [{name: c, keys: [{api-key: k, weight: -1}]}]}",
		"a: 1\na: 2",
	} {
		if _, err := Translate([]byte(y)); err == nil {
			t.Errorf("%q: no error", y)
		}
	}
}

// Text settings written as numbers are read as their digits, as today's core reads them; the
// management key is used exactly as written.
func TestScalarsAndExactKeys(t *testing.T) {
	r := translate(t, `
access: {api-keys: [123456]}
management: {secret-key: " spaced ", allow-remote: true}
claude-api-key:
  - api-key: 987654
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"max_wait_ms": 30000},
		"client_keys": ["123456"],
		"management_key": " spaced ",
		"extensions": {"pluxy/claude": {"accounts": [{"id": "claude-api-key-1", "credential": {"kind": "api_key", "api_key": "987654"}}]}}
	}`)
}

// A key with no api-key (today's core serves an upstream that needs none) is not supported
// yet: it adds no account and is named, and the rest of the file still loads.
func TestKeysWithoutAPIKey(t *testing.T) {
	r := translate(t, `
gemini-api-key:
  - base-url: http://127.0.0.1:9001
  - api-key: g
openai-compatibility:
  - name: local
    base-url: http://127.0.0.1:1234/v1
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"max_wait_ms": 30000},
		"extensions": {"pluxy/gemini": {"accounts": [{"id": "gemini-api-key-2", "credential": {"kind": "api_key", "api_key": "g"}}]}}
	}`)
	n := notices(r)
	for _, want := range []string{"api-keys.gemini keys without an api-key", "api-keys.openai-compatibility keys without an api-key"} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
}
