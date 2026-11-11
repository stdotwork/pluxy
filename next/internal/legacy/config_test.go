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
		"routing": {"request_retry": 3, "max_wait_ms": 30000},
		"usage": {"enabled": false}
	}`)
	if r.AuthDir != "~/.cli-proxy-api" {
		t.Errorf("AuthDir = %q", r.AuthDir)
	}
	// The example spells out today's default identifier, so imported SIWC accounts get it.
	if want := (ImportOptions{CodexIdentity: true, ImageGenerationTool: true, Images: true, SIWCIdentifier: "CLIProxyAPI"}); r.Import != want {
		t.Errorf("Import = %+v", r.Import)
	}
	// Everything the example sets is supported, off or at today's default.
	if len(r.Notices) != 0 {
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
		Gemini:           {"gemini-1-1", "gemini-1-2", "gemini-2", "vertex-1", "interactions-1"},
		XAI:              {"xai-1"},
		OpenAICompatible: {"openrouter-1", "openrouter-2"},
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("account IDs = %v, want %v", ids, want)
	}
	vertex := exts[Gemini].(map[string]any)["accounts"].([]any)[3]
	equalJSON(t, vertex, `{
		"id": "vertex-1", "weight": 5, "prefix": "test", "disable_cooling": false, "request_retry": 3,
		"models": [{"name": "gemini-2.5-flash", "alias": "vertex-flash"}, {"name": "gemini-2.5-pro", "alias": "vertex-pro"}],
		"excluded_models": ["imagen-3.0-generate-002", "imagen-*"],
		"settings": {"base_url": "https://example.com/api", "vertex": {}},
		"credential": {"kind": "api_key", "api_key": "vk-123..."}
	}`)
	// A key's null fields inherit the group's; its own weight, proxy and request-retry are its
	// own, and its empty excluded-models clears the group's.
	first := exts[Gemini].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	equalJSON(t, first["excluded_models"], `["gemini-2.5-pro", "gemini-2.5-*", "*-preview", "*flash*"]`)
	second := exts[Gemini].(map[string]any)["accounts"].([]any)[1]
	equalJSON(t, second, `{
		"id": "gemini-1-2", "priority": 0, "weight": 2, "prefix": "test", "disable_cooling": false, "request_retry": 0,
		"models": [{"name": "gemini-2.5-flash", "alias": "gemini-flash"}],
		"settings": {"base_url": "https://generativelanguage.googleapis.com"},
		"credential": {"kind": "api_key", "api_key": "replace-with-second-gemini-key"}
	}`)
	// The Interactions key group is a Gemini account that serves the Interactions API.
	interactions := exts[Gemini].(map[string]any)["accounts"].([]any)[4]
	equalJSON(t, interactions, `{
		"id": "interactions-1", "weight": 5, "prefix": "native", "disable_cooling": false, "request_retry": 3,
		"models": [{"name": "gemini-2.5-flash", "alias": "native-gemini-flash"}],
		"excluded_models": ["gemini-2.5-pro"],
		"settings": {"base_url": "https://generativelanguage.googleapis.com", "interactions": true},
		"credential": {"kind": "api_key", "api_key": "AIzaSy...03"}
	}`)
	n := notices(r)
	for _, want := range []string{
		"api-keys.xai.websockets", "api-keys.meta",
		"api-keys.claude.cloak (making other clients look like Claude Code is out of scope)",
	} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
	if strings.Contains(n, "unknown") || strings.Contains(n, "experimental-cch-signing") {
		t.Errorf("notices name an unknown or dead key:\n%s", n)
	}
	for _, read := range []string{"prefix", "models.alias", "excluded-models"} {
		if strings.Contains(n, "."+read) {
			t.Errorf("notices name %s, which is read:\n%s", read, n)
		}
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
    alpha-search: true
    weight: 3
    priority: 2
claude-api-key:
  - api-key: "claude-key"
    disable-codex-cloaking: true
    rebuild-mid-system-message: true
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
		"management_allow_remote": true,
		"routing": {"strategy": "weighted", "max_accounts": 2, "request_retry": 0, "session_affinity": {"ttl_ms": 600000}},
		"extensions": {
			"pluxy/codex": {"accounts": [{
				"id": "codex-api-key-1", "priority": 2, "weight": 3,
				"settings": {"base_url": "https://codex.example.com", "websockets": true, "alpha_search": true, "image_generation_tool": false},
				"credential": {"kind": "api_key", "api_key": "codex-key"}
			}]},
			"pluxy/claude": {"accounts": [{
				"id": "claude-api-key-1", "settings": {"rebuild_mid_system_message": true},
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
	if r.Import != (ImportOptions{CodexIdentity: false, ImageGenerationTool: false, Images: true}) {
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
		"management_allow_remote": true,
		"routing": {"request_retry": 0},
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
		"not supported yet, ignored: routing.session-affinity-rebind-on, upstream.claude.tool-search\n",
		"dropped, ignored: plugins (replaced by packages), server.commercial-mode (it turns off features this core does not have), upstream.claude.session-date-pin-days (making other clients look like Claude Code is out of scope)\n",
		"unknown keys, ignored: server.discovery-typo\n",
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
token_count_estimate: false
packages: {dir: pk}
cors: {allowed_origins: ["http://localhost:5173"]}
extensions:
  pluxy/codex: {idle_stop_ms: 0, accounts: [{id: own, credential: {kind: api_key, api_key: k}}]}
codex-api-key: [{api-key: from-yaml}]
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"request_retry": 0},
		"credentials_dir": "creds", "drop_policy": "reject", "token_count_estimate": false, "packages": {"dir": "pk"},
		"cors": {"allowed_origins": ["http://localhost:5173"]},
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
		"codex-api-key: [{api-key: k, alpha-search: sometimes}]",
		"claude-api-key: [{api-key: k, rebuild-mid-system-message: sometimes}]",
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
		"listen": ":8317", "routing": {"request_retry": 0},
		"client_keys": ["123456"],
		"management_key": " spaced ",
		"management_allow_remote": true,
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
		"listen": ":8317", "routing": {"request_retry": 0},
		"extensions": {"pluxy/gemini": {"accounts": [{"id": "gemini-api-key-2", "credential": {"kind": "api_key", "api_key": "g"}}]}}
	}`)
	n := notices(r)
	for _, want := range []string{"api-keys.gemini keys without an api-key", "api-keys.openai-compatibility keys without an api-key"} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
}

// TestModelNaming covers today's model naming settings: per-key model aliases, prefixes and
// exclusions, force-model-prefix, and the per-channel oauth-model-alias and
// oauth-excluded-models, which go to the package of each channel's accounts.
func TestModelNaming(t *testing.T) {
	r := translate(t, `
force-model-prefix: true
oauth-model-alias:
  codex:
    - {name: gpt-6.1-sol, alias: sol}
    - {name: gpt-6-astra, alias: Astra, fork: true, display-name: Astra Display}
    - {name: gpt-6-astra, alias: astra}
    - {name: same, alias: SAME}
    - {name: gpt-6-luna, alias: luna, force-mapping: true}
  kimi-ai:
    - {name: kimi-k2, alias: k2}
  gemini-cli:
    - {name: gemini-2.5-pro, alias: pro}
oauth-excluded-models:
  codex: [" GPT-IMAGE-* ", "gpt-image-*"]
  qwen: [qwen-max]
codex-api-key:
  - api-key: k1
    prefix: " /team/ "
    excluded-models: ["*-Mini"]
    models:
      - {name: up-a, alias: pub-a}
      - {name: up-b}
      - {name: up-c, alias: pub-a}
openai-compatibility:
  - name: compat
    base-url: https://compat.example.com/v1
    prefix: a/b
    excluded-models: [x]
    api-key-entries: [{api-key: k2}]
    models: [{name: m, alias: m}]
`)
	equalJSON(t, r.Config["routing"], `{"force_model_prefix": true, "request_retry": 0}`)
	exts := asJSON(t, r.Config["extensions"]).(map[string]any)
	equalJSON(t, exts[Codex].(map[string]any)["accounts"], `[{
		"id": "codex-api-key-1", "prefix": "team", "excluded_models": ["*-mini"],
		"models": [{"name": "up-a", "alias": "pub-a"}, "up-b"],
		"credential": {"kind": "api_key", "api_key": "k1"}
	}]`)
	equalJSON(t, exts[Codex].(map[string]any)["model_alias"], `[
		{"name": "gpt-6.1-sol", "alias": "sol"},
		{"name": "gpt-6-astra", "alias": "Astra", "fork": true, "display_name": "Astra Display"},
		{"name": "gpt-6-luna", "alias": "luna"}
	]`)
	equalJSON(t, exts[Codex].(map[string]any)["excluded_models"], `["gpt-image-*"]`)
	equalJSON(t, exts[Kimi], `{"model_alias": [{"name": "kimi-k2", "alias": "k2"}]}`)
	// openai-compatibility has no excluded-models in today's core, and a prefix with a slash
	// inside is none.
	equalJSON(t, exts[OpenAICompatible].(map[string]any)["accounts"], `[{
		"id": "compat", "models": ["m"],
		"settings": {"base_url": "https://compat.example.com/v1"},
		"credential": {"kind": "api_key", "api_key": "k2"}
	}]`)
	n := notices(r)
	for _, want := range []string{"oauth.model-alias.force-mapping", "oauth.model-alias.gemini-cli: no extension", "oauth.excluded-models.qwen: no extension"} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
	if strings.Contains(n, "unknown") || strings.Contains(n, "force-model-prefix") || strings.Contains(n, "excluded-models,") {
		t.Errorf("notices name a key that is read:\n%s", n)
	}
}

// server.tls (older spelling tls) becomes the tls section when it is enabled.
func TestTLS(t *testing.T) {
	want := map[string]any{"enable": true, "cert": "certs/c.pem", "key": "/etc/k.pem"}
	for _, yaml := range []string{
		"server:\n  tls: {enable: true, cert: certs/c.pem, key: /etc/k.pem}\n",
		"tls:\n  enable: true\n  cert: certs/c.pem\n  key: /etc/k.pem\n",
	} {
		r := translate(t, yaml)
		if got := r.Config["tls"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: tls = %#v, want %#v", yaml, got, want)
		}
		if strings.Contains(notices(r), "tls") {
			t.Errorf("%s: notices name tls:\n%s", yaml, notices(r))
		}
	}
	if r := translate(t, "server:\n  tls: {enable: false, cert: c.pem, key: k.pem}\n"); r.Config["tls"] != nil {
		t.Errorf("tls off: tls = %#v, want none", r.Config["tls"])
	}
	if _, err := Translate([]byte("server:\n  tls: {enable: yes-please}\n")); err == nil {
		t.Error("a tls.enable that is not true or false was accepted")
	}
}

// server.trusted-proxies (older spelling trusted-proxies) becomes trusted_proxies.
func TestTrustedProxies(t *testing.T) {
	for _, yaml := range []string{
		"server:\n  trusted-proxies: [10.0.0.0/8, 127.0.0.1]\n",
		"trusted-proxies:\n  - 10.0.0.0/8\n  - 127.0.0.1\n",
	} {
		r := translate(t, yaml)
		if got := asJSON(t, r.Config["trusted_proxies"]); !reflect.DeepEqual(got, []any{"10.0.0.0/8", "127.0.0.1"}) {
			t.Errorf("%s: trusted_proxies = %#v", yaml, got)
		}
		if strings.Contains(notices(r), "trusted-proxies") {
			t.Errorf("%s: notices name trusted-proxies:\n%s", yaml, notices(r))
		}
	}
	if _, err := Translate([]byte("server:\n  trusted-proxies: 10.0.0.1\n")); err == nil {
		t.Error("trusted-proxies that is not a list was accepted")
	}
}

// quota-response-mode, under its v8 name or the older spelling, letter case and spaces aside, as
// today's core reads it; retryable is the default and adds nothing; another value fails the load.
func TestQuotaResponseMode(t *testing.T) {
	for in, want := range map[string]any{
		"routing: {cooldown: {quota-response-mode: usage-limit}}":     "usage-limit",
		"routing: {cooldown: {quota-response-mode: ' Usage-Limit '}}": "usage-limit",
		"quota-cooldown-response-mode: usage-limit":                   "usage-limit",
		"routing: {cooldown: {quota-response-mode: retryable}}":       nil,
		"routing: {cooldown: {quota-response-mode: ''}}":              nil,
	} {
		r := translate(t, in)
		var got any
		if m, ok := asJSON(t, r.Config["routing"]).(map[string]any); ok {
			got = m["quota_response_mode"]
		}
		if got != want {
			t.Errorf("%s: quota_response_mode = %v, want %v", in, got, want)
		}
		if strings.Contains(notices(r), "quota-response-mode") {
			t.Errorf("%s: notices name it:\n%s", in, notices(r))
		}
	}
	if _, err := Translate([]byte("routing: {cooldown: {quota-response-mode: stop}}")); err == nil || !strings.Contains(err.Error(), "quota-response-mode") {
		t.Errorf("an unknown mode loaded: %v", err)
	}
}

// A key's request-retry is its account's request_retry; a group's counts for keys that set none;
// an openai-compatibility group's counts for all its keys, which have none of their own; a
// negative one inherits routing's count.
func TestKeyRequestRetry(t *testing.T) {
	r := translate(t, `
api-keys:
  codex:
    - request-retry: 2
      keys:
        - api-key: a
        - api-key: b
          request-retry: 0
        - api-key: c
          request-retry: -1
  openai-compatibility:
    - name: group
      base-url: http://127.0.0.1:1/v1
      request-retry: 3
      keys:
        - api-key: d
          request-retry: 1
`)
	got := map[string]any{}
	for _, ext := range []string{"pluxy/codex", "pluxy/openai-compatible"} {
		x := asJSON(t, r.Config["extensions"]).(map[string]any)[ext].(map[string]any)
		for _, a := range x["accounts"].([]any) {
			a := a.(map[string]any)
			got[a["credential"].(map[string]any)["api_key"].(string)] = a["request_retry"]
		}
	}
	want := map[string]any{"a": 2.0, "b": 0.0, "c": nil, "d": 3.0} // c: a key's own negative count inherits routing's, not its group's
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request_retry by key = %v, want %v", got, want)
	}
	if strings.Contains(notices(r), "request-retry") {
		t.Errorf("notices name it:\n%s", notices(r))
	}
	if _, err := Translate([]byte("codex-api-key: [{api-key: k, request-retry: soon}]")); err == nil {
		t.Error("a request-retry that is not a number loaded")
	}
}

// disable-image-generation true turns off both the hosted image tool and the Images API on Codex
// accounts; chat and passthrough keep the Images API, as today's core keeps its image routes.
func TestDisableImageGeneration(t *testing.T) {
	for v, want := range map[string]ImportOptions{
		"true":        {CodexIdentity: true, ImageGenerationTool: false, Images: false},
		"chat":        {CodexIdentity: true, ImageGenerationTool: false, Images: true},
		"passthrough": {CodexIdentity: true, ImageGenerationTool: false, Images: true},
		"false":       DefaultImportOptions,
	} {
		r := translate(t, "disable-image-generation: "+v)
		if r.Import != want {
			t.Errorf("%s: Import = %+v, want %+v", v, r.Import, want)
		}
	}
}

// codex.signin-with-chatgpt-identifier (and its v8 spelling) reaches the import options, trimmed,
// and is no longer reported as unsupported.
func TestSIWCIdentifier(t *testing.T) {
	for _, y := range []string{
		"codex:\n  signin-with-chatgpt-identifier: '  my-agent '",
		"oauth:\n  providers:\n    codex:\n      signin-with-chatgpt-identifier: my-agent",
	} {
		r := translate(t, y)
		if r.Import.SIWCIdentifier != "my-agent" {
			t.Errorf("%q: SIWCIdentifier = %q", y, r.Import.SIWCIdentifier)
		}
		if strings.Contains(notices(r), "signin-with-chatgpt-identifier") {
			t.Errorf("%q: notices name it:\n%s", y, notices(r))
		}
	}
	if r := translate(t, "port: 8317"); r.Import.SIWCIdentifier != "" {
		t.Errorf("default: %q", r.Import.SIWCIdentifier)
	}
	if _, err := Translate([]byte("codex:\n  signin-with-chatgpt-identifier: [a]")); err == nil {
		t.Error("an identifier that is not text loaded")
	}
}

// An interactions-api-key entry (older spelling) and an api-keys.interactions group (v8) are Gemini
// accounts with "interactions": true, with the usual fields as the gemini section maps them, and
// no notice.
func TestInteractionsKeys(t *testing.T) {
	r := translate(t, `
interactions-api-key:
  - name: native
    api-key: k1
    weight: 3
    base-url: https://generativelanguage.googleapis.com
    prefix: native
    disable-cooling: true
    request-retry: 2
    models: [{name: gemini-3-pro-preview, alias: pro}]
    excluded-models: [gemini-x]
`)
	equalJSON(t, r.Config["extensions"], `{"pluxy/gemini": {"accounts": [{
		"id": "native", "weight": 3, "prefix": "native", "disable_cooling": true, "request_retry": 2,
		"models": [{"name": "gemini-3-pro-preview", "alias": "pro"}], "excluded_models": ["gemini-x"],
		"settings": {"base_url": "https://generativelanguage.googleapis.com", "interactions": true},
		"credential": {"kind": "api_key", "api_key": "k1"}
	}]}}`)
	if n := notices(r); n != "" {
		t.Errorf("notices:\n%s", n)
	}
	r = translate(t, `
api-keys:
  interactions:
    - name: v8
      base-url: https://example.com/api
      keys: [{api-key: k2, priority: 4}]
`)
	equalJSON(t, r.Config["extensions"], `{"pluxy/gemini": {"accounts": [{
		"id": "v8", "priority": 4,
		"settings": {"base_url": "https://example.com/api", "interactions": true},
		"credential": {"kind": "api_key", "api_key": "k2"}
	}]}}`)
	if n := notices(r); n != "" {
		t.Errorf("notices:\n%s", n)
	}
}

// A vertex key's interactions: true turns on the Interactions API for its Vertex account, as the
// setting "interactions": true beside "vertex"; false or absent does not, and a text "true" is
// refused, as other boolean settings are.
func TestVertexInteractions(t *testing.T) {
	r := translate(t, `
api-keys:
  vertex:
    - name: on
      keys: [{api-key: vk-on, interactions: true}]
    - name: off
      keys: [{api-key: vk-off, interactions: false}]
    - name: unset
      keys: [{api-key: vk-unset}]
`)
	equalJSON(t, r.Config["extensions"], `{"pluxy/gemini": {"accounts": [
		{"id": "on", "settings": {"vertex": {}, "interactions": true}, "credential": {"kind": "api_key", "api_key": "vk-on"}},
		{"id": "off", "settings": {"vertex": {}}, "credential": {"kind": "api_key", "api_key": "vk-off"}},
		{"id": "unset", "settings": {"vertex": {}}, "credential": {"kind": "api_key", "api_key": "vk-unset"}}
	]}}`)
	if n := notices(r); n != "" {
		t.Errorf("notices:\n%s", n)
	}
	// The older spelling holds interactions in its entry, which oldGroup moves into the key.
	r = translate(t, "vertex-api-key:\n  - api-key: vk-old\n    interactions: true\n")
	equalJSON(t, r.Config["extensions"], `{"pluxy/gemini": {"accounts": [
		{"id": "vertex-api-key-1", "settings": {"vertex": {}, "interactions": true}, "credential": {"kind": "api_key", "api_key": "vk-old"}}
	]}}`)
	if _, err := Translate([]byte("api-keys: {vertex: [{name: v, keys: [{api-key: k, interactions: sometimes}]}]}")); err == nil {
		t.Error("an interactions that is not true or false loaded")
	}
	if _, err := Translate([]byte("api-keys: {vertex: [{name: v, keys: [{api-key: k, interactions: \"true\"}]}]}")); err == nil {
		t.Error("an interactions written as text loaded")
	}
}

// claude.model-level-cooling (each spelling) becomes model_level_cooling on the Claude keys'
// accounts and on imported Claude accounts; off, it adds nothing.
func TestClaudeModelLevelCooling(t *testing.T) {
	for _, y := range []string{
		"claude:\n  model-level-cooling: true\n",
		"upstream:\n  claude:\n    model-level-cooling: true\n",
		"oauth:\n  providers:\n    claude:\n      model-level-cooling: true\n",
	} {
		r := translate(t, y+"claude-api-key:\n  - api-key: claude-key\n")
		equalJSON(t, r.Config["extensions"], `{"pluxy/claude": {"accounts": [{
			"id": "claude-api-key-1", "settings": {"model_level_cooling": true},
			"credential": {"kind": "api_key", "api_key": "claude-key"}
		}]}}`)
		if !r.Import.ClaudeModelLevelCooling {
			t.Errorf("%q: Import = %+v", y, r.Import)
		}
		if strings.Contains(notices(r), "model-level-cooling") {
			t.Errorf("%q: notices name it:\n%s", y, notices(r))
		}
	}
	r := translate(t, "claude:\n  model-level-cooling: false\nclaude-api-key:\n  - api-key: claude-key\n")
	equalJSON(t, r.Config["extensions"], `{"pluxy/claude": {"accounts": [{"id": "claude-api-key-1", "credential": {"kind": "api_key", "api_key": "claude-key"}}]}}`)
	if r.Import.ClaudeModelLevelCooling {
		t.Errorf("off: Import = %+v", r.Import)
	}
}
