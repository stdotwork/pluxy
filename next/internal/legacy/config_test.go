package legacy

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
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
		"routing": {"request_retry": 3, "max_wait_ms": 30000, "session_affinity": {"enabled": false}},
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
		"proxy_url": "socks5://proxy.example.com:1080", "headers": {"X-Custom-Header": "custom-value"},
		"models": [{"name": "gemini-2.5-flash", "alias": "vertex-flash"}, {"name": "gemini-2.5-pro", "alias": "vertex-pro"}],
		"excluded_models": ["imagen-3.0-generate-002", "imagen-*"],
		"settings": {"base_url": "https://example.com/api", "vertex": {}},
		"credential": {"kind": "api_key", "api_key": "vk-123..."}
	}`)
	// A key's null fields inherit the group's (its null headers the group's), its own weight,
	// proxy and request-retry are its own, and its empty excluded-models clears the group's.
	first := exts[Gemini].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	equalJSON(t, first["excluded_models"], `["gemini-2.5-pro", "gemini-2.5-*", "*-preview", "*flash*"]`)
	equalJSON(t, first["proxy_url"], `"socks5://proxy.example.com:1080"`)
	second := exts[Gemini].(map[string]any)["accounts"].([]any)[1]
	equalJSON(t, second, `{
		"id": "gemini-1-2", "priority": 0, "weight": 2, "prefix": "test", "disable_cooling": false, "request_retry": 0,
		"proxy_url": "direct", "headers": {"X-Custom-Header": "custom-value"},
		"models": [{"name": "gemini-2.5-flash", "alias": "gemini-flash"}],
		"settings": {"base_url": "https://generativelanguage.googleapis.com"},
		"credential": {"kind": "api_key", "api_key": "replace-with-second-gemini-key"}
	}`)
	// The Interactions key group is a Gemini account that serves the Interactions API.
	interactions := exts[Gemini].(map[string]any)["accounts"].([]any)[4]
	equalJSON(t, interactions, `{
		"id": "interactions-1", "weight": 5, "prefix": "native", "disable_cooling": false, "request_retry": 3,
		"proxy_url": "socks5://proxy.example.com:1080", "headers": {"X-Custom-Header": "custom-value"},
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
	for _, read := range []string{"prefix", "models.alias", "excluded-models", "proxy-url", "headers"} {
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

// Today's core keeps no Codex API key without a base-url: the key is dropped and named in the
// notices. A key takes its group's base-url when it has none of its own; other providers' keys
// without one stay.
func TestCodexKeyWithoutBaseURLIsDropped(t *testing.T) {
	r := translate(t, `
codex-api-key:
  - api-key: no-url
  - api-key: blank-url
    base-url: "   "
  - api-key: has-url
    base-url: https://codex.example.com
claude-api-key:
  - api-key: claude-key
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"request_retry": 0, "session_affinity": {"enabled": false}},
		"extensions": {
			"pluxy/codex": {"accounts": [
				{"id": "codex-api-key-3", "settings": {"base_url": "https://codex.example.com"}, "credential": {"kind": "api_key", "api_key": "has-url"}}
			]},
			"pluxy/claude": {"accounts": [{"id": "claude-api-key-1", "credential": {"kind": "api_key", "api_key": "claude-key"}}]}
		}
	}`)
	for _, id := range []string{"codex-api-key-1", "codex-api-key-2"} {
		if want := "key " + id + " has no base-url: dropped"; !strings.Contains(notices(r), want) {
			t.Errorf("notices lack %q:\n%s", want, notices(r))
		}
	}
	if n := notices(r); strings.Contains(n, "codex-api-key-3") || strings.Contains(n, "claude") {
		t.Errorf("notices name a key that stays:\n%s", n)
	}

	r = translate(t, `
api-keys:
  codex:
    - name: grouped
      base-url: https://group.example.com
      keys: [{api-key: inherits}, {api-key: own, base-url: https://own.example.com}]
    - name: bare
      keys: [{api-key: bare-key}]
`)
	equalJSON(t, r.Config["extensions"], `{"pluxy/codex": {"accounts": [
		{"id": "grouped-1", "settings": {"base_url": "https://group.example.com"}, "credential": {"kind": "api_key", "api_key": "inherits"}},
		{"id": "grouped-2", "settings": {"base_url": "https://own.example.com"}, "credential": {"kind": "api_key", "api_key": "own"}}
	]}}`)
	if n := notices(r); !strings.Contains(n, "api-keys.codex key bare has no base-url: dropped") || strings.Contains(n, "grouped") {
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
    base-url: https://old.example.com
api-keys:
  codex:
    - name: c
      base-url: https://new.example.com
      keys: [{api-key: new-key}]
`)
	equalJSON(t, r.Config, `{
		"listen": ":9001",
		"management_key": "new",
		"management_allow_remote": true,
		"routing": {"request_retry": 0, "session_affinity": {"enabled": false}},
		"extensions": {"pluxy/codex": {"accounts": [{"id": "c", "settings": {"base_url": "https://new.example.com"}, "credential": {"kind": "api_key", "api_key": "new-key"}}]}}
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
client: {codex: {enable-apply-patch: true}}
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
		"not supported yet, ignored: client.codex.enable-apply-patch (built when someone uses a non-Codex model through the Codex client and needs apply_patch on it)\n",
		"dropped, ignored: plugins (replaced by packages), routing.session-affinity-rebind-on (no need found), server.commercial-mode (it turns off features this core does not have), upstream.claude.session-date-pin-days (making other clients look like Claude Code is out of scope), upstream.claude.tool-search (Codex's tool search on Claude accounts: off by default, with no panel or app control)\n",
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

// TestDecidedDroppedSettings: settings decided not to be built are named as dropped, with their
// reason, and never as "not supported yet"; the settings that are still to come stay Later, each
// with what would bring it.
func TestDecidedDroppedSettings(t *testing.T) {
	r := translate(t, `
server:
  response-compression: {enabled: true, min-bytes: 10}
  discovery: {enabled: true, service-name: x}
routing: {session-affinity-rebind-on: quota, session-affinity-subagents: false}
oauth:
  request-scoped-errors: [{status: 400}]
  providers:
    antigravity: {sensitive-words: [x], connection-pool: {enabled: true}}
    devin: {sensitive-words: [x]}
antigravity-signature-bypass-strict: true
api-keys:
  meta: [{api-key: m}]
  openai-compatibility:
    - name: compat
      base-url: https://compat.example.com/v1
      support-prompt-cache-key: true
      connection-pool: {enabled: true}
      request-scoped-errors: [{status: 400}]
      keys: [{api-key: k}]
      models:
        - {name: m, alias: m, display-name: D, max-context-length: 9, input-modalities: [text], output-modalities: [text], is-compat: true, use-max-completion-tokens: true, thinking: {levels: [low]}, force-mapping: true, image: true}
  vertex:
    - name: v
      keys: [{api-key: k, openapi: true, openapi-location: us-central1}]
      models: [{name: m, support-configuration-update: true, openapi: true}]
  xai:
    - name: x
      websockets: true
      keys: [{api-key: k}]
client: {codex: {enable-apply-patch: true, optimize-multi-agent-v2: true}}
upstream:
  codex: {upstream-request-compression: true, upstream-request-compression-min-bytes: 2048, stream-bootstrap-buffering: true, stream-bootstrap-timeout: 5s}
multimedia: {gpt-image-2-base-model: gpt-6-image, video-result-auth-cache-ttl: 1h}
models: {catalog: "https://example.com/models.json", codex-catalog: /etc/codex.json, devin-catalog: "https://example.com/devin.json"}
`)
	var later, dropped string
	for _, n := range r.Notices {
		if rest, ok := strings.CutPrefix(n, "config.yaml: not supported yet, ignored: "); ok {
			later = rest
		}
		if rest, ok := strings.CutPrefix(n, "config.yaml: dropped, ignored: "); ok {
			dropped = rest
		}
	}
	for _, want := range []string{
		"server.response-compression (", "server.discovery.enabled (", "routing.session-affinity-rebind-on (",
		"oauth.request-scoped-errors (", "oauth.providers.antigravity (the Antigravity extension does not read these settings)", "oauth.providers.devin (", "api-keys.meta (",
		"api-keys.openai-compatibility.support-prompt-cache-key", "api-keys.openai-compatibility.connection-pool",
		"api-keys.openai-compatibility.request-scoped-errors",
		"api-keys.openai-compatibility.models.display-name", "api-keys.openai-compatibility.models.max-context-length",
		"api-keys.openai-compatibility.models.input-modalities", "api-keys.openai-compatibility.models.output-modalities",
		"api-keys.openai-compatibility.models.is-compat", "api-keys.openai-compatibility.models.use-max-completion-tokens",
		"api-keys.openai-compatibility.models.thinking",
		"api-keys.openai-compatibility.models.image", "api-keys.vertex.models.support-configuration-update",
		"api-keys.xai.websockets", "routing.session-affinity-subagents (",
		"upstream.codex.upstream-request-compression (", "upstream.codex.upstream-request-compression-min-bytes (",
		"multimedia.gpt-image-2-base-model (", "multimedia.video-result-auth-cache-ttl (",
		"models.catalog (", "models.codex-catalog (", "models.devin-catalog (",
	} {
		if !strings.Contains(dropped, want) {
			t.Errorf("not named as dropped: %q\n%s", want, notices(r))
		}
		if strings.Contains(later, want) {
			t.Errorf("named as not supported yet: %q\n%s", want, notices(r))
		}
	}
	for _, want := range []string{
		"api-keys.openai-compatibility.models.force-mapping (built when", "api-keys.vertex.openapi (built when",
		"api-keys.vertex.openapi-location (built when", "api-keys.vertex.models.openapi (built when",
		"client.codex.enable-apply-patch (built when", "client.codex.optimize-multi-agent-v2 (built when",
		"upstream.codex.stream-bootstrap-buffering (built when", "upstream.codex.stream-bootstrap-timeout (built when",
	} {
		if !strings.Contains(later, want) {
			t.Errorf("not named as not supported yet: %q\n%s", want, notices(r))
		}
		if strings.Contains(dropped, want) {
			t.Errorf("named as dropped: %q\n%s", want, notices(r))
		}
	}
}

// TestSessionAffinitySubagents: true is today's default and asks for nothing; false asks to spread
// subagents over the pool, which is not done here, so it is named.
func TestSessionAffinitySubagents(t *testing.T) {
	for _, v := range []string{"true", "false"} {
		r := translate(t, "routing: {session-affinity: true, session-affinity-subagents: "+v+"}")
		got := strings.Contains(notices(r), "routing.session-affinity-subagents")
		if got != (v == "false") {
			t.Errorf("subagents %s: named = %v\n%s", v, got, notices(r))
		}
	}
}

// TestContainerSettings: a setting of a container that has no rule of its own is an unknown key,
// not one "not supported yet".
func TestContainerSettings(t *testing.T) {
	r := translate(t, "observability: {logs: {mystery: 1}}\nmodels: {mystery: 1}\n")
	n := notices(r)
	if !strings.Contains(n, "unknown keys, ignored: ") || !strings.Contains(n, "observability.logs.mystery") || !strings.Contains(n, "models.mystery") {
		t.Errorf("notices:\n%s", n)
	}
	if strings.Contains(n, "not supported yet") {
		t.Errorf("an unknown key is called not supported yet:\n%s", n)
	}
}

// A bcrypt hash of the management key cannot be used as the key: the translation says so and
// leaves the key out (core refuses to serve the config; pluxy import cpa makes a new key).
func TestHashedManagementKey(t *testing.T) {
	r := translate(t, `remote-management: {secret-key: "$2a$10$abcdefghijklmnopqrstuuAbCdEfGhIjKlMnOpQrStUvWxYz01234"}`)
	if _, ok := r.Config["management_key"]; ok || !r.HashedManagementKey {
		t.Errorf("a bcrypt hash: config %v, hashed %v; want no management_key and the hash noted", r.Config, r.HashedManagementKey)
	}
}

func TestStrategyNames(t *testing.T) {
	for in, want := range map[string]any{
		"round-robin": nil, "fillfirst": "fill-first", "ff": "fill-first", "wrr": "weighted",
		"weighted": "weighted", "least-active": "least-active", "soonest-reset": "soonest-reset", "nonsense": nil,
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
codex-api-key: [{api-key: from-yaml, base-url: "https://yaml.example.com"}]
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"request_retry": 0, "session_affinity": {"enabled": false}},
		"credentials_dir": "creds", "drop_policy": "reject", "token_count_estimate": false, "packages": {"dir": "pk"},
		"cors": {"allowed_origins": ["http://localhost:5173"]},
		"extensions": {"pluxy/codex": {"idle_stop_ms": 0, "accounts": [
			{"id": "own", "credential": {"kind": "api_key", "api_key": "k"}},
			{"id": "codex-api-key-1", "settings": {"base_url": "https://yaml.example.com"}, "credential": {"kind": "api_key", "api_key": "from-yaml"}}
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
		"listen": ":8317", "routing": {"request_retry": 0, "session_affinity": {"enabled": false}},
		"client_keys": ["123456"],
		"management_key": " spaced ",
		"management_allow_remote": true,
		"extensions": {"pluxy/claude": {"accounts": [{"id": "claude-api-key-1", "credential": {"kind": "api_key", "api_key": "987654"}}]}}
	}`)
}

// A text setting written as a scalar YAML reads as something else (a number in any base, an
// exponent, infinity, true, a date, a number too big for 64 bits) is its text as written, as
// today's core's typed decoder keeps it: its loader (config.LoadConfigOptional) reads each of
// these into its string fields unchanged.
func TestTextKeptAsWritten(t *testing.T) {
	for _, s := range []string{
		"0123", "1e5", "0x1F", "0o17", "0b101", ".inf", "-.Inf", ".NaN", "true", "False", "1_000", "+12", "1.0", "-0",
		"2001-12-14", "9223372036854775808", "123456789012345678901234567890",
	} {
		t.Run(s, func(t *testing.T) {
			r := translate(t, `
server:
  host: `+s+`
  tls:
    enable: true
    cert: `+s+`
    key: `+s+`
  trusted-proxies:
    - `+s+`
routing:
  strategy: `+s+`
  session-affinity: true
  session-affinity-ttl: `+s+`
observability:
  pprof:
    enable: true
    addr: `+s+`
requests:
  payload:
    override:
      - models:
          - name: `+s+`
            protocol: `+s+`
            from-protocol: `+s+`
            headers:
              X-H: `+s+`
            exist:
              - `+s+`
            not-exist:
              - `+s+`
        params:
          p: x
    default:
      - models:
          - name: `+s+`
        params:
          p: x
    default-raw:
      - models:
          - name: `+s+`
        params:
          p: '"x"'
    override-raw:
      - models:
          - name: `+s+`
        params:
          p: '"x"'
    filter:
      - models:
          - name: `+s+`
            protocol: `+s+`
        params:
          - `+s+`
access:
  api-keys:
    - `+s+`
management:
  secret-key: `+s+`
oauth:
  auth-dir: `+s+`
  providers:
    codex:
      signin-with-chatgpt-identifier: `+s+`
  model-alias:
    claude:
      - name: `+s+`
        alias: alias-1
        display-name: `+s+`
      - name: name-2
        alias: `+s+`
  excluded-models:
    claude:
      - `+s+`
claude-api-key:
  - api-key: `+s+`
    prefix: `+s+`
    base-url: `+s+`
    headers:
      X-A: `+s+`
    models:
      - name: `+s+`
        alias: alias-4
      - name: name-3
        alias: `+s+`
    excluded-models:
      - `+s+`
api-keys:
  codex:
    - name: `+s+`
      base-url: https://codex.example.com
      keys:
        - api-key: `+s+`
`)
			lower := strings.ToLower(s)
			want := map[string]any{
				"listen":          net.JoinHostPort(s, "8317"),
				"tls":             map[string]any{"enable": true, "cert": s, "key": s},
				"trusted_proxies": []any{s},
				// Not a duration, so the default hour, as in today's core.
				"routing": map[string]any{"request_retry": 0, "session_affinity": map[string]any{"ttl_ms": 3600000}},
				"pprof":   map[string]any{"enable": true, "addr": s},
				"payload": map[string]any{
					"override": []any{map[string]any{
						"models": []any{map[string]any{"name": s, "protocol": s, "from-protocol": s, "headers": map[string]any{"X-H": s},
							"exist": []any{s}, "not-exist": []any{s}}},
						"params": map[string]any{"p": "x"},
					}},
					"default":      []any{map[string]any{"models": []any{map[string]any{"name": s}}, "params": map[string]any{"p": "x"}}},
					"default-raw":  []any{map[string]any{"models": []any{map[string]any{"name": s}}, "params": map[string]any{"p": `"x"`}}},
					"override-raw": []any{map[string]any{"models": []any{map[string]any{"name": s}}, "params": map[string]any{"p": `"x"`}}},
					"filter":       []any{map[string]any{"models": []any{map[string]any{"name": s, "protocol": s}}, "params": []any{s}}},
				},
				"client_keys":    []any{s},
				"management_key": s,
				"extensions": map[string]any{
					Claude: map[string]any{
						"model_alias":     []any{map[string]any{"name": s, "alias": "alias-1", "display_name": s}, map[string]any{"name": "name-2", "alias": s}},
						"excluded_models": []any{lower},
						"accounts": []any{map[string]any{
							"id": "claude-api-key-1", "credential": map[string]any{"kind": "api_key", "api_key": s},
							"prefix": s, "settings": map[string]any{"base_url": s}, "headers": map[string]any{"X-A": s},
							"models":          []any{map[string]any{"name": s, "alias": "alias-4"}, map[string]any{"name": "name-3", "alias": s}},
							"excluded_models": []any{lower},
						}},
					},
					Codex: map[string]any{"accounts": []any{map[string]any{
						"id": AccountID(s), "credential": map[string]any{"kind": "api_key", "api_key": s},
						"settings": map[string]any{"base_url": "https://codex.example.com"},
					}}},
				},
			}
			wb, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			equalJSON(t, r.Config, string(wb))
			if r.AuthDir != s || r.Import.SIWCIdentifier != s {
				t.Errorf("AuthDir = %q, SIWCIdentifier = %q, want %q", r.AuthDir, r.Import.SIWCIdentifier, s)
			}
			if n := notices(r); !strings.Contains(n, fmt.Sprintf("routing.strategy %q is not known", s)) {
				t.Errorf("notices do not name the strategy %q:\n%s", s, n)
			}
			// A quota response mode no such text is valid for is refused, naming the text as
			// written.
			y := "routing: {cooldown: {quota-response-mode: " + s + "}}"
			if _, err := Translate([]byte(y)); err == nil || !strings.Contains(err.Error(), strconv.Quote(s)) {
				t.Errorf("%s: err = %v, want it to name %q", y, err, s)
			}
			// No such text is a proxy URL either, so the proxy URL check refuses a proxy-url (its
			// error does not show a value that is not a URL, so it cannot show which text was
			// read, but it is the URL check's, not a refusal to read the value as text).
			for _, y := range []string{"requests: {proxy-url: " + s + "}", "claude-api-key: [{api-key: k, proxy-url: " + s + "}]"} {
				if _, err := Translate([]byte(y)); err == nil || !strings.Contains(err.Error(), "proxy-url \"(not a valid proxy URL)\"") {
					t.Errorf("%s: err = %v, want the proxy URL check's error", y, err)
				}
			}
		})
	}
	// Payload values (a rule's params, a model's match) are values, as in today's core.
	r0 := translate(t, "payload:\n  override:\n    - models: [{name: m, match: [{a: 0123}]}]\n      params: {p: 0123, q: true, r: 1e5}\n")
	equalJSON(t, r0.Config["payload"], `{"override": [{"models": [{"name": "m", "match": [{"a": 83}]}], "params": {"p": 83, "q": true, "r": 100000}}]}`)
	// Null is no text, as in today's core: no key, so no account.
	r := translate(t, "access: {api-keys: [k]}\nclaude-api-key:\n  - api-key: ~\n")
	if _, ok := r.Config["extensions"]; ok {
		t.Errorf("api-key: ~ made an account: %v", r.Config["extensions"])
	}
}

// Aliases and merge keys read as yaml.v3 reads them, text kept as written through them: a key's
// own field wins over a merged one, and an earlier mapping of a merged list over a later one. A
// file yaml.v3 refuses (repeated keys, excessive aliasing) is refused.
func TestAliasesAndMergeKeys(t *testing.T) {
	r := translate(t, `
x-first: &first {base-url: 0x1F, prefix: first}
x-second: &second {base-url: second, prefix: second, proxy-url: "http://proxy.example:8080"}
claude-api-key:
  - <<: [*first, *second]
    api-key: &k 0123
    prefix: own
access: {api-keys: [*k]}
`)
	equalJSON(t, r.Config, `{
		"listen": ":8317", "routing": {"request_retry": 0, "session_affinity": {"enabled": false}},
		"client_keys": ["0123"],
		"extensions": {"pluxy/claude": {"accounts": [{"id": "claude-api-key-1", "credential": {"kind": "api_key", "api_key": "0123"},
			"prefix": "own", "proxy_url": "http://proxy.example:8080", "settings": {"base_url": "0x1F"}}]}}
	}`)
	bomb := "a: &a [x, x, x, x, x, x, x, x, x, x]\n"
	for i, prev := 1, "a"; i < 9; i++ {
		name := string(rune('a' + i))
		bomb += name + ": &" + name + " [" + strings.Repeat("*"+prev+", ", 9) + "*" + prev + "]\n"
		prev = name
	}
	for _, y := range []string{"claude-api-key: [{api-key: 0123, api-key: 0124}]", bomb} {
		if _, err := Translate([]byte(y)); err == nil {
			t.Errorf("%.60q: no error", y)
		}
	}
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
		"listen": ":8317", "routing": {"request_retry": 0, "session_affinity": {"enabled": false}},
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
  antigravity:
    - {name: gemini-3.8-flash-high, alias: ag}
    - {name: claude-opus-5-5-thinking, alias: opus, display-name: Opus}
  gemini-cli:
    - {name: gemini-2.5-pro, alias: pro}
oauth-excluded-models:
  codex: [" GPT-IMAGE-* ", "gpt-image-*"]
  antigravity: [" Gemini-2.5-* "]
  qwen: [qwen-max]
codex-api-key:
  - api-key: k1
    base-url: https://codex.example.com
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
	equalJSON(t, r.Config["routing"], `{"force_model_prefix": true, "request_retry": 0, "session_affinity": {"enabled": false}}`)
	exts := asJSON(t, r.Config["extensions"]).(map[string]any)
	equalJSON(t, exts[Codex].(map[string]any)["accounts"], `[{
		"id": "codex-api-key-1", "prefix": "team", "excluded_models": ["*-mini"],
		"settings": {"base_url": "https://codex.example.com"},
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
	// The archived Antigravity extension's accounts take their channel's rules like any other's.
	equalJSON(t, exts[Antigravity], `{
		"model_alias": [{"name": "gemini-3.8-flash-high", "alias": "ag"}, {"name": "claude-opus-5-5-thinking", "alias": "opus", "display_name": "Opus"}],
		"excluded_models": ["gemini-2.5-*"]
	}`)
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

// Entries of an openai-compatibility group that share an alias are a pool in today's core. The
// import keeps the first and names the alias and every member it drops; a model listed twice
// loses nothing and is not named, and in the other sections, where today's core also takes the
// first entry, nothing is named.
func TestAliasPoolBecomesAChain(t *testing.T) {
	r := translate(t, `
codex-api-key:
  - api-key: k1
    base-url: https://codex.example.com
    models:
      - {name: up-a, alias: pub-a}
      - {name: up-c, alias: pub-a}
openai-compatibility:
  - name: compat
    base-url: https://compat.example.com/v1
    api-key-entries: [{api-key: k2}]
    models:
      - {name: deepseek-v3.1, alias: claude-opus-4.66}
      - {name: glm-5, alias: claude-opus-4.66}
      - {name: kimi-k2.5, alias: claude-opus-4.66}
      - {name: glm-5, alias: claude-opus-4.66}
      - {name: plain}
      - {name: plain}
`)
	exts := asJSON(t, r.Config["extensions"]).(map[string]any)
	// Each model of the pool is served under its own name, which the chain's steps name.
	equalJSON(t, exts[OpenAICompatible].(map[string]any)["accounts"], `[{
		"id": "compat", "models": ["plain", "deepseek-v3.1", "glm-5", "kimi-k2.5"],
		"settings": {"base_url": "https://compat.example.com/v1"},
		"credential": {"kind": "api_key", "api_key": "k2"}
	}]`)
	equalJSON(t, asJSON(t, r.Config["routing"]).(map[string]any)["chains"], `{"claude-opus-4.66": [
		{"model": "deepseek-v3.1", "provider": "pluxy/openai-compatible", "accounts": ["compat"]},
		{"model": "glm-5", "provider": "pluxy/openai-compatible", "accounts": ["compat"]},
		{"model": "kimi-k2.5", "provider": "pluxy/openai-compatible", "accounts": ["compat"]}
	]}`)
	n := notices(r)
	for _, want := range []string{`openai-compatibility key compat`, `alias "claude-opus-4.66"`, `lists 3 upstream models`, `fallback chain routing.chains.claude-opus-4.66`} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
	// Only openai-compatibility pools: today's core keeps the first entry elsewhere.
	if strings.Contains(n, "plain") || strings.Contains(n, "pub-a") {
		t.Errorf("notices name more than the one pool:\n%s", n)
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
      base-url: https://codex.example.com
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

// upstream.xai.inject-x-search, and its older spellings (oauth.providers.xai.inject-x-search and
// the top-level xai), is an import option like Codex's: the xAI API-key accounts of the config get
// "inject_x_search": true, the imported xAI logins do too (see TestXAIInjectXSearchImport), and it
// is off by default. The v8 value wins over an older one.
func TestXAIInjectXSearch(t *testing.T) {
	const keys = "api-keys: {xai: [{name: grok, keys: [{api-key: xk-1}]}]}"
	account := func(on bool) string {
		if on {
			return `{"id": "grok", "settings": {"inject_x_search": true}, "credential": {"kind": "api_key", "api_key": "xk-1"}}`
		}
		return `{"id": "grok", "credential": {"kind": "api_key", "api_key": "xk-1"}}`
	}
	for _, tc := range []struct {
		name, yaml string
		on         bool
	}{
		{"v8", "upstream: {xai: {inject-x-search: true}}", true},
		{"older oauth.providers", "oauth: {providers: {xai: {inject-x-search: true}}}", true},
		{"older top-level xai", "xai: {inject-x-search: true}", true},
		{"false", "upstream: {xai: {inject-x-search: false}}", false},
		{"unset", "port: 8317", false},
		{"v8 wins over older", "upstream: {xai: {inject-x-search: false}}\noauth: {providers: {xai: {inject-x-search: true}}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := translate(t, tc.yaml+"\n"+keys)
			equalJSON(t, r.Config["extensions"], `{"pluxy/xai": {"accounts": [`+account(tc.on)+`]}}`)
			equalJSON(t, r.Import, fmt.Sprintf(`{"codex_identity": true, "image_generation_tool": true, "images": true, "inject_x_search": %t}`, tc.on))
			if n := notices(r); strings.Contains(n, "inject-x-search") {
				t.Errorf("notices name the setting, which is supported:\n%s", n)
			}
		})
	}
	for _, y := range []string{
		"upstream: {xai: {inject-x-search: \"true\"}}",
		"oauth: {providers: {xai: {inject-x-search: sometimes}}}",
		"upstream: {xai: true}",
	} {
		if _, err := Translate([]byte(y)); err == nil {
			t.Errorf("%q loaded", y)
		}
	}
}

// Two names of one header that differ only in spaces or letter case make the file fail to load,
// naming both and no value, instead of one of them being kept by chance.
func TestProviderHeadersRefuseSameHeaderTwice(t *testing.T) {
	_, err := Translate([]byte(`
claude-api-key:
  - api-key: k1
    base-url: https://gw.example
    headers: {X-Gateway-Key: fake-gw-one, " X-Gateway-Key ": fake-gw-two}
`))
	if err == nil {
		t.Fatal("a headers mapping with the same header twice loaded")
	}
	for _, want := range []string{`"X-Gateway-Key"`, `" X-Gateway-Key "`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
	if strings.Contains(err.Error(), "fake-gw") {
		t.Errorf("error shows a value: %q", err)
	}
}

// A provider key's headers, as today's core reads them: values that are numbers or true or false
// are their text, names and values are trimmed, empty ones are dropped, and a value today's core
// takes from the client's request is named and not imported. In the older layout an entry's
// headers are its key's; an openai-compatibility group's are each of its keys'.
func TestProviderHeaders(t *testing.T) {
	r := translate(t, `
claude-api-key:
  - api-key: k1
    base-url: https://gw.example
    headers: {" X-Gateway-Key ": " fake-gw ", X-Port: 8080, X-On: true, X-Empty: "", X-Copy: "$X-Client", X-Session: "s-$CPA-SESSION-ID"}
openai-compatibility:
  - name: relay
    base-url: https://relay.example/v1
    headers: {X-Relay: one}
    api-key-entries:
      - api-key: k2
      - api-key: k3
`)
	exts := asJSON(t, r.Config["extensions"]).(map[string]any)
	claude := exts[Claude].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	equalJSON(t, claude["headers"], `{"X-Gateway-Key": "fake-gw", "X-Port": "8080", "X-On": "true"}`)
	for _, a := range exts[OpenAICompatible].(map[string]any)["accounts"].([]any) {
		equalJSON(t, a.(map[string]any)["headers"], `{"X-Relay": "one"}`)
	}
	n := notices(r)
	for _, name := range []string{"X-Copy", "X-Session"} {
		if !strings.Contains(n, "header "+name+" takes its value from the client's request") {
			t.Errorf("notices do not name %s:\n%s", name, n)
		}
	}
	if strings.Contains(n, "fake-gw") || strings.Contains(n, ".headers") {
		t.Errorf("notices name the headers setting or a value:\n%s", n)
	}
}

// Payload rules go across with today's names, from either spelling, and no notice names them.
func TestPayload(t *testing.T) {
	rules := `
    override:
      - models:
          - name: "gpt-5.4-fast"
            protocol: "codex"
        params:
          service_tier: priority
    filter:
      - models:
          - name: "gpt-*"
            protocol: "codex"
        params:
          - 'tools.#(name=="apply_patch")'
`
	want := `{"override": [{"models": [{"name": "gpt-5.4-fast", "protocol": "codex"}], "params": {"service_tier": "priority"}}],
		"filter": [{"models": [{"name": "gpt-*", "protocol": "codex"}], "params": ["tools.#(name==\"apply_patch\")"]}]}`
	for _, doc := range []string{"requests:\n  payload:" + strings.ReplaceAll(rules, "\n    ", "\n      "), "payload:" + rules} {
		r := translate(t, doc)
		equalJSON(t, r.Config["payload"], want)
		if n := notices(r); strings.Contains(n, "payload") {
			t.Errorf("notices name payload:\n%s", n)
		}
	}
	if _, err := Translate([]byte("requests:\n  payload: [1]\n")); err == nil || !strings.Contains(err.Error(), "requests.payload must be a mapping") {
		t.Errorf("a payload list: err = %v", err)
	}
}

// Today's core reads a config.yaml with yaml.v3, which matches a key to its field exactly: a key
// written in another letter case, or not a payload key at all, is dropped, and the rule keeps
// what else it has (a rule without models matches nothing; a model without a name matches
// nothing). The importer drops the same keys and names each in a notice, where the JSON config's
// decoder would have matched them case-insensitively and applied them. Each want is what today's
// core's loader (config.ParseConfigBytes) kept of the same file.
func TestPayloadKeysExact(t *testing.T) {
	const rule = "    - models: [{name: m}]\n      params: {p: 1}\n"
	for _, c := range []struct{ name, payload, want, notice string }{
		{"list", "  Override:\n" + rule, `{}`, "requests.payload.Override (today's core reads override)"},
		{"raw list", "  OVERRIDE-RAW:\n" + rule, `{}`, "requests.payload.OVERRIDE-RAW (today's core reads override-raw)"},
		{"filter list", "  Filter:\n    - models: [{name: m}]\n      params: [a]\n", `{}`, "requests.payload.Filter (today's core reads filter)"},
		{"models", "  override:\n    - Models: [{name: m}]\n      params: {p: 1}\n", `{"override": [{"params": {"p": 1}}]}`, "requests.payload.override[0].Models (today's core reads models)"},
		{"params", "  override:\n    - models: [{name: m}]\n      Params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}]}]}`, "requests.payload.override[0].Params (today's core reads params)"},
		{"name", "  override:\n    - models: [{Name: m, protocol: codex}]\n      params: {p: 1}\n", `{"override": [{"models": [{"protocol": "codex"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Name (today's core reads name)"},
		{"name as a number", "  override:\n    - models: [{Name: 0123, protocol: codex}]\n      params: {p: 1}\n", `{"override": [{"models": [{"protocol": "codex"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Name (today's core reads name)"},
		{"protocol", "  override:\n    - models: [{name: m, Protocol: codex}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Protocol (today's core reads protocol)"},
		{"from-protocol", "  override:\n    - models: [{name: m, From-Protocol: claude}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].From-Protocol (today's core reads from-protocol)"},
		{"headers", "  override:\n    - models: [{name: m, Headers: {X-A: '*'}}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Headers (today's core reads headers)"},
		{"match", "  override:\n    - models: [{name: m, Match: [{a: 1}]}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Match (today's core reads match)"},
		{"not-match", "  override:\n    - models: [{name: m, Not-Match: [{a: 1}]}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Not-Match (today's core reads not-match)"},
		{"exist", "  override:\n    - models: [{name: m, Exist: [a]}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].Exist (today's core reads exist)"},
		{"not-exist", "  override:\n    - models: [{name: m, NOT-EXIST: [a]}]\n      params: {p: 1}\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`, "requests.payload.override[0].models[0].NOT-EXIST (today's core reads not-exist)"},
		{"filter params", "  filter:\n    - models: [{name: m}]\n      Params: [a]\n", `{"filter": [{"models": [{"name": "m"}]}]}`, "requests.payload.filter[0].Params (today's core reads params)"},
		{"filter models", "  filter:\n    - Models: [{name: m}]\n      params: [a]\n", `{"filter": [{"params": ["a"]}]}`, "requests.payload.filter[0].Models (today's core reads models)"},
		{"unknown keys", "  override:\n    - models: [{name: m, bogus: 1}]\n      params: {p: 1}\n      extra: 2\n  other: []\n", `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`,
			"requests.payload.other (not a payload key), requests.payload.override[0].extra (not a payload key), requests.payload.override[0].models[0].bogus (not a payload key)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			nested := "  " + strings.ReplaceAll(strings.TrimSuffix(c.payload, "\n"), "\n", "\n  ") + "\n"
			for _, doc := range []string{"requests:\n  payload:\n" + nested, "payload:\n" + c.payload} {
				r := translate(t, doc)
				equalJSON(t, r.Config["payload"], c.want)
				if n := notices(r); !strings.Contains(n, "payload keys ignored, as today's core ignores them (it matches key names exactly): ") || !strings.Contains(n, c.notice) {
					t.Errorf("notices do not say %q:\n%s", c.notice, n)
				}
			}
		})
	}
	// The exact spellings carry no notice about keys.
	r := translate(t, "payload:\n  override:\n"+rule)
	if n := notices(r); strings.Contains(n, "payload") {
		t.Errorf("notices name payload for exact keys:\n%s", n)
	}
	// Only the payload's own keys are matched exactly: a path, a header name and a match key are
	// the user's text and keep their case.
	r = translate(t, "payload:\n  override:\n    - models: [{name: m, headers: {X-Mixed-Case: '*'}, match: [{Mixed.Path: 1}], exist: [Mixed.Path]}]\n      params: {Mixed.Path: 1}\n")
	equalJSON(t, r.Config["payload"], `{"override": [{"models": [{"name": "m", "headers": {"X-Mixed-Case": "*"}, "match": [{"Mixed.Path": 1}], "exist": ["Mixed.Path"]}], "params": {"Mixed.Path": 1}}]}`)
	if n := notices(r); strings.Contains(n, "payload") {
		t.Errorf("notices name payload for user text:\n%s", n)
	}
}

// A -raw rule's param goes across as today's core sent it upstream (found by running its loader
// and its payloadRawValue on the same file): a string is the JSON text itself; any other value
// is what json.Marshal makes of it, so a date YAML reads as one is the JSON string of its
// RFC 3339 form; a param that is null, or that json.Marshal refuses (.inf, .nan, also inside a
// list), is skipped, and the rule's other params still apply. The importer carries the first
// two and drops the skipped ones, naming each.
func TestPayloadRawParams(t *testing.T) {
	for _, c := range []struct{ name, params, want, notice string }{
		{"date", "{p: 2024-01-15}", `{"p": "\"2024-01-15T00:00:00Z\""}`, ""},
		{"date and time", "{p: 2024-01-15T10:30:00Z}", `{"p": "\"2024-01-15T10:30:00Z\""}`, ""},
		{"date and time with fractions", "{p: 2024-01-15T10:30:00.123456789Z}", `{"p": "\"2024-01-15T10:30:00.123456789Z\""}`, ""},
		{"date in a list", "{p: [2024-01-15, 1]}", `{"p": ["2024-01-15T00:00:00Z", 1]}`, ""},
		{"date in a mapping", "{p: {d: 2024-01-15}}", `{"p": {"d": "2024-01-15T00:00:00Z"}}`, ""},
		{"+inf", "{p: .inf, q: 1}", `{"q": 1}`, `requests.payload.override-raw[0].params["p"]: not JSON (json: unsupported value: +Inf), dropped: today's core skipped the param`},
		{"-inf", "{p: -.inf, q: 1}", `{"q": 1}`, `requests.payload.override-raw[0].params["p"]: not JSON (json: unsupported value: -Inf), dropped: today's core skipped the param`},
		{"nan", "{p: .nan, q: 1}", `{"q": 1}`, `requests.payload.override-raw[0].params["p"]: not JSON (json: unsupported value: NaN), dropped: today's core skipped the param`},
		{"inf in a list", "{p: [.inf], q: 1}", `{"q": 1}`, `requests.payload.override-raw[0].params["p"]: not JSON (json: unsupported value: +Inf), dropped: today's core skipped the param`},
		{"null", "{p: ~, q: 1}", `{"q": 1}`, `requests.payload.override-raw[0].params["p"]: null, dropped: today's core skipped the param`},
		{"values and JSON text", `{p: 5, q: 0123, r: 1e5, s: true, t: 1.50, u: '{"a":1}'}`, `{"p": 5, "q": 83, "r": 100000, "s": true, "t": 1.5, "u": "{\"a\":1}"}`, ""},
	} {
		for _, list := range []string{"override-raw", "default-raw"} {
			t.Run(c.name+" in "+list, func(t *testing.T) {
				r := translate(t, "payload:\n  "+list+":\n    - models: [{name: m}]\n      params: "+c.params+"\n")
				equalJSON(t, r.Config["payload"], `{"`+list+`": [{"models": [{"name": "m"}], "params": `+c.want+`}]}`)
				n := notices(r)
				if c.notice == "" {
					if strings.Contains(n, "payload") {
						t.Errorf("notices name payload:\n%s", n)
					}
					return
				}
				want := strings.ReplaceAll(c.notice, "override-raw", list)
				if !strings.Contains(n, "config.yaml: "+want) {
					t.Errorf("notices do not say %q:\n%s", want, n)
				}
			})
		}
	}
	// A param of a rule that is not a -raw one keeps its value as it is: a date stays a date.
	r := translate(t, "payload:\n  override:\n    - models: [{name: m}]\n      params: {p: 2024-01-15}\n")
	equalJSON(t, r.Config["payload"], `{"override": [{"models": [{"name": "m"}], "params": {"p": "2024-01-15T00:00:00Z"}}]}`)
}

// Today's core drops a -raw rule as a whole when a param of it is a string that is empty or not
// JSON once trimmed, with a warning in its log, and goes on loading; the rest of the list stays.
// The importer drops the same rule and names it. Each case is what today's core's loader kept
// (and, for the last, applied) of the same file.
func TestPayloadRawStringNotJSON(t *testing.T) {
	rule := func(list, params string) string {
		return "payload:\n  " + list + ":\n    - models: [{name: m}]\n      params: " + params + "\n"
	}
	const dropped = `requests.payload.override-raw[0]: param "p" is a string that is not JSON, rule dropped: today's core dropped the whole rule and logged a warning`
	for _, c := range []struct{ name, doc, want, notice string }{
		{"a date in quotes", rule("override-raw", "{p: '2024-01-15'}"), `{"override-raw": []}`, dropped},
		{"a word", rule("override-raw", "{p: foo}"), `{"override-raw": []}`, dropped},
		{"an empty string", rule("override-raw", "{p: ''}"), `{"override-raw": []}`, dropped},
		{"spaces only", rule("override-raw", "{p: '   '}"), `{"override-raw": []}`, dropped},
		{"a date with a zone after a space, which YAML reads as text", rule("override-raw", "{p: 2024-01-15 10:30:00 +02:00}"), `{"override-raw": []}`, dropped},
		{"another param that is fine", rule("override-raw", "{p: foo, q: 1}"), `{"override-raw": []}`, dropped},
		{"default-raw", rule("default-raw", "{p: foo, q: 1}"), `{"default-raw": []}`, strings.ReplaceAll(dropped, "override-raw", "default-raw")},
		{"padded JSON text is kept as written", rule("override-raw", "{p: ' 5 '}"), `{"override-raw": [{"models": [{"name": "m"}], "params": {"p": " 5 "}}]}`, ""},
		{"only that rule goes", "payload:\n  override-raw:\n    - models: [{name: m}]\n      params: {p: foo}\n    - models: [{name: m}]\n      params: {q: 1}\n",
			`{"override-raw": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, dropped},
		{"only that list loses it", "payload:\n  default-raw:\n    - models: [{name: m}]\n      params: {p: '1'}\n    - models: [{name: m}]\n      params: {q: ''}\n",
			`{"default-raw": [{"models": [{"name": "m"}], "params": {"p": "1"}}]}`, `requests.payload.default-raw[1]: param "q" is a string that is not JSON, rule dropped`},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := translate(t, c.doc)
			equalJSON(t, r.Config["payload"], c.want)
			n := notices(r)
			if c.notice == "" && strings.Contains(n, "payload") || c.notice != "" && !strings.Contains(n, "config.yaml: "+c.notice) {
				t.Errorf("notices do not say %q:\n%s", c.notice, n)
			}
		})
	}
}

// A value json.Marshal refuses (.inf and .nan, alone or in a list or mapping) outside a -raw
// rule, as today's core's payload code handled it (found by running it on each case):
//   - in a param of override or default it skipped the param, or, for a top-level .inf, .nan or
//     -.inf in default and -.inf in override, wrote +Inf, NaN or -Inf into the body, which is not
//     JSON; the importer drops the param either way, since there is no value to carry;
//   - in a match, the model never matched, and in a not-match, the condition never equalled and
//     so never excluded a request; a model of a filter rule is read the same way.
//
// Each of these used to fail the whole load with a bare "json: unsupported value".
func TestPayloadNonJSONValues(t *testing.T) {
	const wrote = "dropped: today's core skipped the param or wrote invalid JSON into the body"
	rule := func(list, params string) string {
		return "payload:\n  " + list + ":\n    - models: [{name: m}]\n      params: " + params + "\n"
	}
	model := func(list, m string) string {
		params := "{p: 1}"
		if list == "filter" {
			params = "[a]"
		}
		return "payload:\n  " + list + ":\n    - models: [" + m + "]\n      params: " + params + "\n"
	}
	const (
		noModels   = `{"override": [{"models": [], "params": {"p": 1}}]}`
		oneModel   = `{"override": [{"models": [{"name": "m"}], "params": {"p": 1}}]}`
		infMatch   = `requests.payload.override[0].models[0]: match[0]["a"] is not JSON (json: unsupported value: +Inf), model dropped: today's core never matched it`
		infNotMach = `requests.payload.override[0].models[0]: not-match[0]["a"] is not JSON (json: unsupported value: +Inf), condition dropped: it never equalled a value in today's core`
	)
	for _, c := range []struct{ name, doc, want, notice string }{
		{"override +inf", rule("override", "{p: .inf, q: 1}"), `{"override": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `requests.payload.override[0].params["p"]: not JSON (json: unsupported value: +Inf), ` + wrote},
		{"override -inf", rule("override", "{p: -.inf, q: 1}"), `{"override": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `not JSON (json: unsupported value: -Inf), ` + wrote},
		{"override nan", rule("override", "{p: .nan, q: 1}"), `{"override": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `not JSON (json: unsupported value: NaN), ` + wrote},
		{"override inf in a list", rule("override", "{p: [.inf, 1], q: 1}"), `{"override": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `requests.payload.override[0].params["p"]: not JSON`},
		{"override inf in a mapping", rule("override", "{p: {a: .inf}, q: 1}"), `{"override": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `requests.payload.override[0].params["p"]: not JSON`},
		{"default +inf", rule("default", "{p: .inf, q: 1}"), `{"default": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `requests.payload.default[0].params["p"]: not JSON (json: unsupported value: +Inf), ` + wrote},
		{"default nan", rule("default", "{p: .nan, q: 1}"), `{"default": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `not JSON (json: unsupported value: NaN), ` + wrote},
		{"default inf in a list", rule("default", "{p: [.inf], q: 1}"), `{"default": [{"models": [{"name": "m"}], "params": {"q": 1}}]}`, `requests.payload.default[0].params["p"]: not JSON`},
		{"match +inf", model("override", "{name: m, match: [{a: .inf}]}"), noModels, infMatch},
		{"match -inf", model("override", "{name: m, match: [{a: -.inf}]}"), noModels, `match[0]["a"] is not JSON (json: unsupported value: -Inf), model dropped`},
		{"match nan", model("override", "{name: m, match: [{a: .nan}]}"), noModels, `match[0]["a"] is not JSON (json: unsupported value: NaN), model dropped`},
		{"match inf in a list", model("override", "{name: m, match: [{a: [.inf]}]}"), noModels, `match[0]["a"] is not JSON`},
		{"match inf beside a value", model("override", "{name: m, match: [{a: .inf, b: 1}]}"), noModels, `match[0]["a"] is not JSON`},
		{"match inf drops only its model", model("override", "{name: m, match: [{a: .inf}]}, {name: m}"), oneModel, infMatch},
		{"filter match inf", model("filter", "{name: m, match: [{a: .inf}]}"), `{"filter": [{"models": [], "params": ["a"]}]}`, `requests.payload.filter[0].models[0]: match[0]["a"] is not JSON`},
		{"not-match inf", model("override", "{name: m, not-match: [{a: .inf}]}"), `{"override": [{"models": [{"name": "m", "not-match": [{}]}], "params": {"p": 1}}]}`, infNotMach},
		{"not-match inf beside a value", model("override", "{name: m, not-match: [{a: .inf, b: 1}]}"), `{"override": [{"models": [{"name": "m", "not-match": [{"b": 1}]}], "params": {"p": 1}}]}`, infNotMach},
		{"filter not-match inf", model("filter", "{name: m, not-match: [{a: .inf}]}"), `{"filter": [{"models": [{"name": "m", "not-match": [{}]}], "params": ["a"]}]}`, `requests.payload.filter[0].models[0]: not-match[0]["a"] is not JSON`},
		// Dates are fine in these places: today's core sends and compares the JSON string of the date.
		{"date param", rule("override", "{p: 2024-01-15}"), `{"override": [{"models": [{"name": "m"}], "params": {"p": "2024-01-15T00:00:00Z"}}]}`, ""},
		{"date in match", model("override", "{name: m, match: [{a: 2024-01-15}]}"), `{"override": [{"models": [{"name": "m", "match": [{"a": "2024-01-15T00:00:00Z"}]}], "params": {"p": 1}}]}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := translate(t, c.doc)
			equalJSON(t, r.Config["payload"], c.want)
			if n := notices(r); c.notice == "" && strings.Contains(n, "payload") || !strings.Contains(n, c.notice) {
				t.Errorf("notices do not say %q:\n%s", c.notice, n)
			}
		})
	}
	// A value JSON cannot hold where today's core reads another type fails the load by naming the
	// rules (today's core refuses the file too), not with a bare "json: unsupported value".
	_, err := Translate([]byte("payload:\n  override:\n    - models: [{name: m, match: .inf}]\n      params: {p: 1}\n"))
	if err == nil || !strings.Contains(err.Error(), "requests.payload: a value is not JSON where today's core reads another type") {
		t.Errorf("a match that is .inf: err = %v", err)
	}
}

// A config.yaml keeps today's default for session affinity, off: the translated config says so,
// since a JSON config without the section has it on. With the key on it is as today's core has it.
func TestSessionAffinityStaysOffWithoutItsKey(t *testing.T) {
	for in, want := range map[string]string{
		"{}":                                   `{"enabled": false}`,
		"routing: {strategy: fill-first}":      `{"enabled": false}`,
		"routing: {session-affinity: false}":   `{"enabled": false}`,
		"routing: {session-affinity-ttl: 10m}": `{"enabled": false}`,
		"routing: {session-affinity: true}":    `{"ttl_ms": 3600000}`,
		"routing: {session-affinity: true, session-affinity-ttl: 10m}": `{"ttl_ms": 600000}`,
	} {
		r := translate(t, in)
		routing, _ := asJSON(t, r.Config["routing"]).(map[string]any)
		equalJSON(t, routing["session_affinity"], want)
	}
}
