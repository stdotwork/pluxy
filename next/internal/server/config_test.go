package server

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

// An extension timing too large for a time.Duration is refused, not wrapped into a negative
// duration.
func TestConfigRefusesHugeTimings(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a"}],"restart":{"stable_ms":9223372036855}}}`
	if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); err == nil || !strings.Contains(err.Error(), "timings") {
		t.Errorf("ParseConfig = %v, want an error about the timings", err)
	}
}

// An account that lists two upstream models under one name is refused, naming the alias and the
// account, where it was used to serve the first and ignore the rest. The same model listed
// twice, and one name per model, still load.
func TestConfigRefusesRepeatedModelAlias(t *testing.T) {
	cfg := func(models string) string {
		return `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"pool","models":` + models + `}]}}`
	}
	_, err := ParseConfig([]byte(cfg(`[{"name":"up-a","alias":"pub"},{"name":"up-b","alias":"pub"}]`)), "/tmp/config.json")
	if err == nil || !strings.Contains(err.Error(), `"pub"`) || !strings.Contains(err.Error(), `"pool"`) {
		t.Errorf("ParseConfig = %v, want a refusal naming the alias \"pub\" and the account \"pool\"", err)
	}
	for _, models := range []string{
		`[{"name":"up-a","alias":"pub"},{"name":"up-a","alias":"pub"}]`,
		`[{"name":"up-a","alias":"pub-a"},{"name":"up-b","alias":"pub-b"},"up-c"]`,
	} {
		if _, err := ParseConfig([]byte(cfg(models)), "/tmp/config.json"); err != nil {
			t.Errorf("models %s: ParseConfig = %v, want none", models, err)
		}
	}
}

// token_count_estimate is on unless the config turns it off, in a JSON config and, under the same
// name, in a config.yaml. A value that is not a boolean is refused.
func TestConfigTokenCountEstimate(t *testing.T) {
	const base = `"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a"}]}`
	for _, c := range []struct {
		name, file, cfg string
		want, bad       bool
	}{
		{"absent", "config.json", `{` + base + `}`, true, false},
		{"on", "config.json", `{` + base + `,"token_count_estimate":true}`, true, false},
		{"off", "config.json", `{` + base + `,"token_count_estimate":false}`, false, false},
		{"off in a config.yaml", "config.yaml", "api-keys: [k]\nremote-management: {secret-key: m}\nclaude-api-key: [{api-key: x}]\ntoken_count_estimate: false\n", false, false},
		{"absent in a config.yaml", "config.yaml", "api-keys: [k]\nremote-management: {secret-key: m}\nclaude-api-key: [{api-key: x}]\n", true, false},
		{"not a boolean", "config.json", `{` + base + `,"token_count_estimate":"no"}`, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte(c.cfg), "/tmp/"+c.file)
			if c.bad {
				if err == nil {
					t.Fatal("accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.TokenCountEstimateOn(); got != c.want {
				t.Errorf("TokenCountEstimateOn() = %v, want %v", got, c.want)
			}
		})
	}
}

// A registry is an https:// URL or a path; any other URL is refused, never read as a path.
func TestConfigRegistryLocations(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://example.com/index.json": true,
		"registry/index.json":            true,
		"http://example.com/index.json":  false,
		"ftp://example.com/index.json":   false,
	} {
		cfg := `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"k","packages":{"registries":[{"name":"r","url":"` + url + `"}]}}`
		if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); (err == nil) != ok {
			t.Errorf("registry %q: ParseConfig = %v", url, err)
		}
	}
}

// A registry path is relative to the config file even when the config file's own path is
// relative, as the default "config.json" is.
func TestConfigRegistryPathRelativeToRelativeConfig(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"k","packages":{"registries":[{"name":"r","url":"registry/index.json"}]}}`
	c, err := ParseConfig([]byte(cfg), "config.json")
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if got, want := c.Packages.Registries[0].URL, filepath.Join(wd, "registry", "index.json"); got != want {
		t.Errorf("registry path = %q, want %q", got, want)
	}
}

// A config.yaml is read with today's core's key names and then checked as the JSON config is.
func TestConfigYAMLWithTodaysNames(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg := `
host: 127.0.0.1
port: 9100
api-keys: [client-a]
remote-management: {secret-key: m}
auth-dir: ~/old-auths
claude-api-key:
  - api-key: k
    base-url: http://127.0.0.1:1
    priority: 1
`
	c, err := ParseConfig([]byte(cfg), "/srv/pluxy/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9100" || len(c.ClientKeys) != 1 || c.ClientKeys[0] != "client-a" {
		t.Errorf("listen %q, client keys %v", c.Listen, c.ClientKeys)
	}
	if c.AuthDir != filepath.Join(home, "old-auths") || c.CredentialsDir != "/srv/pluxy/credentials" {
		t.Errorf("auth dir %q, credentials dir %q", c.AuthDir, c.CredentialsDir)
	}
	a := c.Extensions["pluxy/claude"].Accounts
	if len(a) != 1 || a[0].ID != "claude-api-key-1" || a[0].Priority != 1 || a[0].Credential.APIKey != "k" || string(a[0].Settings) != `{"base_url":"http://127.0.0.1:1"}` {
		t.Errorf("claude accounts: %+v", a)
	}
	// Today's core runs no extra rounds and never waits unless request-retry and
	// max-retry-interval say otherwise.
	if r := c.Routing; r.MaxWaitMS != 0 || r.RequestRetry == nil || *r.RequestRetry != 0 {
		t.Errorf("routing %+v, want today's defaults: request_retry 0, max_wait_ms 0", r)
	}

	// A relative auth-dir is relative to the config file.
	c, err = ParseConfig([]byte("auth-dir: auths\napi-keys: [k]\nremote-management: {secret-key: m}\n"), "/srv/pluxy/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != "/srv/pluxy/auths" {
		t.Errorf("auth dir %q", c.AuthDir)
	}
	// A config needs a management key, and no account: accounts can be added by login.
	if _, err := ParseConfig([]byte("auth-dir: auths\napi-keys: [k]\n"), "/srv/pluxy/config.yaml"); err == nil || !strings.Contains(err.Error(), "remote-management: {secret-key:") {
		t.Errorf("a config.yaml with no management key: %v, want a refusal saying how to add one", err)
	}
	if _, err := ParseConfig([]byte("port: 9100\napi-keys: [k]\nremote-management: {secret-key: m}\n"), "/srv/pluxy/config.yaml"); err != nil {
		t.Errorf("a config.yaml with a management key and no account: %v", err)
	}
	// A JSON config is not read as YAML.
	if _, err := ParseConfig([]byte("port: 9100\n"), "/srv/pluxy/config.json"); err == nil {
		t.Error("a config.json in YAML was accepted")
	}
}

// A config.yaml's payload rules load as today's core reads them: the fields it reads as text
// (a model's name, protocol, from-protocol, headers, exist and not-exist, a filter's params)
// keep the text as written, however YAML would read it; a rule's params and a model's match
// keep their values.
func TestConfigYAMLPayloadText(t *testing.T) {
	cfg := `
api-keys: [k]
remote-management: {secret-key: m}
auth-dir: auths
payload:
  override:
    - models:
        - name: 0123
          protocol: codex
          from-protocol: 0x1F
          headers: {X-Pin: 0123, X-Flag: true}
          exist: [0, 1e5]
          not-exist: [2001-12-14]
          match: [{a: 0123}]
        - name: 2001-12-14
      params: {p: 0123, q: true}
  filter:
    - models: [{name: "*"}]
      params: [0, true]
`
	c, err := ParseConfig([]byte(cfg), "/srv/pluxy/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	o := c.Payload.Override[0]
	m := o.Models[0]
	if m.Name != "0123" || m.FromProtocol != "0x1F" || !reflect.DeepEqual(m.Headers, map[string]string{"X-Pin": "0123", "X-Flag": "true"}) ||
		!reflect.DeepEqual(m.Exist, []string{"0", "1e5"}) || !reflect.DeepEqual(m.NotExist, []string{"2001-12-14"}) {
		t.Errorf("model %+v", m)
	}
	if n := o.Models[1].Name; n != "2001-12-14" {
		t.Errorf("model name %q, want 2001-12-14", n)
	}
	if !reflect.DeepEqual(m.Match, []map[string]any{{"a": float64(83)}}) || !reflect.DeepEqual(o.Params, map[string]any{"p": float64(83), "q": true}) {
		t.Errorf("match %v, params %v: want the values 83 and true", m.Match, o.Params)
	}
	if p := c.Payload.Filter[0].Params; !reflect.DeepEqual(p, []string{"0", "true"}) {
		t.Errorf("filter params %q", p)
	}
}

// A config.yaml's payload rules change a request as they did in today's core. The want of each
// case is the body today's core sent upstream for the same file (its loader, then its payload
// code): a key in another letter case was never read, so it changes nothing; a -raw param that
// is a date is sent as the JSON string of its date; a -raw param that is .inf, .nan or null is
// skipped, with the rule's other params applied.
func TestConfigYAMLPayloadLikeOldCore(t *testing.T) {
	for _, c := range []struct{ name, rules, want, notice string }{
		{"exact keys", "override:\n    - models: [{name: m}]\n      params: {p: 1}\n", `{"p":1}`, ""},
		{"list in another case", "Override:\n    - models: [{name: m}]\n      params: {p: 1}\n", `{}`, "payload.Override"},
		{"model name in another case", "override:\n    - models: [{Name: m}]\n      params: {p: 1}\n", `{}`, "payload.override[0].models[0].Name"},
		{"model name as a number in another case", "override:\n    - models: [{Name: 0123}]\n      params: {p: 1}\n", `{}`, "payload.override[0].models[0].Name"},
		{"params in another case", "override:\n    - models: [{name: m}]\n      Params: {p: 1}\n", `{}`, "payload.override[0].Params"},
		{"raw date", "override-raw:\n    - models: [{name: m}]\n      params: {p: 2024-01-15}\n", `{"p":"2024-01-15T00:00:00Z"}`, ""},
		{"default-raw date and time", "default-raw:\n    - models: [{name: m}]\n      params: {p: 2024-01-15T10:30:00Z}\n", `{"p":"2024-01-15T10:30:00Z"}`, ""},
		{"raw .inf", "override-raw:\n    - models: [{name: m}]\n      params: {p: .inf, q: 1}\n", `{"q":1}`, `payload.override-raw[0].params["p"]`},
		{"raw .nan", "default-raw:\n    - models: [{name: m}]\n      params: {p: .nan, q: 1}\n", `{"q":1}`, `payload.default-raw[0].params["p"]`},
		{"raw null", "override-raw:\n    - models: [{name: m}]\n      params: {p: ~, q: 1}\n", `{"q":1}`, `payload.override-raw[0].params["p"]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte("api-keys: [k]\nremote-management: {secret-key: m}\nauth-dir: auths\npayload:\n  "+c.rules), "/srv/pluxy/config.yaml")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := payload.Apply(cfg.Payload, []byte(`{}`), payload.Input{Model: "m"})
			if string(body) != c.want {
				t.Errorf("the body sent is %s, want %s", body, c.want)
			}
			notice := strings.Join(cfg.Notices, "\n")
			if c.notice == "" && strings.Contains(notice, "payload") || !strings.Contains(notice, c.notice) {
				t.Errorf("notices do not name %q:\n%s", c.notice, notice)
			}
		})
	}
}

// A -raw rule with a string param that is not JSON, and a .inf or .nan outside a -raw rule, load
// and change a request as they did in today's core. The want of each case is the body today's
// core sent upstream for the same file (its loader, then ApplyPayloadConfigWithRequest), except
// the two marked: there today's core wrote the invalid JSON text +Inf or -Inf into the body, which
// no upstream accepts, and this core leaves the field out.
func TestConfigYAMLPayloadNonJSONLikeOldCore(t *testing.T) {
	const m = "- models: [{name: m}]\n      params: "
	model := func(list, one string) string {
		return list + ":\n    - models: [" + one + "]\n      params: {p: 1}\n"
	}
	for _, c := range []struct{ name, rules, body, want, notice string }{
		{"a -raw string that is not JSON drops its rule", "override-raw:\n    " + m + "{p: foo, q: 1}\n", `{}`, `{}`, "requests.payload.override-raw[0]"},
		{"a -raw date in quotes drops its rule", "override-raw:\n    " + m + "{p: '2024-01-15'}\n", `{}`, `{}`, "requests.payload.override-raw[0]"},
		{"an empty -raw string drops its rule", "default-raw:\n    " + m + "{p: ''}\n", `{}`, `{}`, "requests.payload.default-raw[0]"},
		{"the other rules of the list stay", "override-raw:\n    " + m + "{p: foo}\n    " + m + "{q: 1}\n", `{}`, `{"q":1}`, "requests.payload.override-raw[0]"},
		{"the other lists stay", "default-raw:\n    " + m + "{p: foo}\n  override-raw:\n    " + m + "{q: 1}\n", `{}`, `{"q":1}`, "requests.payload.default-raw[0]"},
		{"override .inf is skipped", "override:\n    " + m + "{p: .inf, q: 1}\n", `{}`, `{"q":1}`, `requests.payload.override[0].params["p"]`},
		{"override .nan is skipped", "override:\n    " + m + "{p: .nan, q: 1}\n", `{}`, `{"q":1}`, `requests.payload.override[0].params["p"]`},
		{"override .inf in a list is skipped", "override:\n    " + m + "{p: [.inf, 1], q: 1}\n", `{}`, `{"q":1}`, `requests.payload.override[0].params["p"]`},
		{"override .inf in a mapping is skipped", "override:\n    " + m + "{p: {a: .inf}, q: 1}\n", `{}`, `{"q":1}`, `requests.payload.override[0].params["p"]`},
		{"default .inf in a list is skipped", "default:\n    " + m + "{p: [.inf], q: 1}\n", `{}`, `{"q":1}`, `requests.payload.default[0].params["p"]`},
		// Today's core wrote {"p":-Inf,"q":1} and {"p":+Inf,"q":1}.
		{"override -.inf", "override:\n    " + m + "{p: -.inf, q: 1}\n", `{}`, `{"q":1}`, `requests.payload.override[0].params["p"]`},
		{"default .inf", "default:\n    " + m + "{p: .inf, q: 1}\n", `{}`, `{"q":1}`, `requests.payload.default[0].params["p"]`},
		{"a match with .inf never matches", model("override", "{name: m, match: [{a: .inf}]}"), `{"a":1}`, `{"a":1}`, `requests.payload.override[0].models[0]`},
		{"a match with .nan never matches", model("override", "{name: m, match: [{a: .nan}]}"), `{"a":1}`, `{"a":1}`, `requests.payload.override[0].models[0]`},
		{"a match with .inf beside a value never matches", model("override", "{name: m, match: [{a: .inf, b: 1}]}"), `{"a":1,"b":1}`, `{"a":1,"b":1}`, `requests.payload.override[0].models[0]`},
		{"the other models of the rule still match", model("override", "{name: m, match: [{a: .inf}]}, {name: m}"), `{"a":1}`, `{"a":1,"p":1}`, `requests.payload.override[0].models[0]`},
		{"a not-match with .inf excludes nothing", model("override", "{name: m, not-match: [{a: .inf}]}"), `{"a":1}`, `{"a":1,"p":1}`, `requests.payload.override[0].models[0]`},
		{"a not-match with .inf keeps its other paths", model("override", "{name: m, not-match: [{a: .inf, b: 1}]}"), `{"b":1}`, `{"b":1}`, `not-match[0]["a"]`},
		{"a not-match with .inf, its other path free", model("override", "{name: m, not-match: [{a: .inf, b: 1}]}"), `{"b":2}`, `{"b":2,"p":1}`, `not-match[0]["a"]`},
		{"a filter match with .inf never matches", "filter:\n    - models: [{name: m, match: [{a: .inf}]}]\n      params: [a]\n", `{"a":1}`, `{"a":1}`, `requests.payload.filter[0].models[0]`},
		{"a filter not-match with .inf excludes nothing", "filter:\n    - models: [{name: m, not-match: [{a: .inf}]}]\n      params: [a]\n", `{"a":1}`, `{}`, `requests.payload.filter[0].models[0]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte("api-keys: [k]\nremote-management: {secret-key: m}\nauth-dir: auths\npayload:\n  "+c.rules), "/srv/pluxy/config.yaml")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := payload.Apply(cfg.Payload, []byte(c.body), payload.Input{Model: "m"})
			if string(body) != c.want {
				t.Errorf("the body sent for %s is %s, want %s", c.body, body, c.want)
			}
			if notice := strings.Join(cfg.Notices, "\n"); !strings.Contains(notice, c.notice) {
				t.Errorf("notices do not name %q:\n%s", c.notice, notice)
			}
		})
	}
}

// An extension's model_alias and excluded_models apply to its accounts added by login as today's
// oauth-model-alias and oauth-excluded-models apply to auth files: the account's own aliases
// come first and an alias it has is not added again; its own exclusions replace the extension's.
// An account added with an API key gets neither, as today's skip API-key auth files.
func TestLoginDefaults(t *testing.T) {
	x := ExtensionConfig{
		ModelAlias:     []route.ModelAlias{{Name: "m1", Alias: "one"}, {Name: "m2", Alias: "TWO"}},
		ExcludedModels: []string{"x-*"},
	}
	got := x.LoginDefaults(Account{ID: "a", ModelAlias: []route.ModelAlias{{Name: "m3", Alias: "two"}}}, protocol.CredentialOAuth)
	if want := []route.ModelAlias{{Name: "m3", Alias: "two"}, {Name: "m1", Alias: "one"}}; !reflect.DeepEqual(got.ModelAlias, want) {
		t.Errorf("aliases = %+v, want %+v", got.ModelAlias, want)
	}
	if !reflect.DeepEqual(got.ExcludedModels, []string{"x-*"}) {
		t.Errorf("excluded = %v, want the extension's", got.ExcludedModels)
	}
	own := x.LoginDefaults(Account{ID: "b", ExcludedModels: []string{"y"}}, protocol.CredentialOAuth)
	if !reflect.DeepEqual(own.ExcludedModels, []string{"y"}) || len(own.ModelAlias) != 2 {
		t.Errorf("account with its own exclusions: %+v", own)
	}
	if len(x.ModelAlias) != 2 {
		t.Error("LoginDefaults changed the extension's aliases")
	}
	key := x.LoginDefaults(Account{ID: "c"}, protocol.CredentialAPIKey)
	if len(key.ModelAlias) != 0 || len(key.ExcludedModels) != 0 {
		t.Errorf("account added with an API key: %+v, want no extension aliases or exclusions", key)
	}
}

// Core refuses a config without client keys, in both layouts: today's core serves anyone then.
func TestConfigNeedsAClientKey(t *testing.T) {
	for _, c := range []struct{ name, path, cfg string }{
		{"json without client_keys", "config.json", `{"listen":"127.0.0.1:0","management_key":"k"}`},
		{"json with an empty list", "config.json", `{"listen":"127.0.0.1:0","client_keys":[],"management_key":"k"}`},
		{"yaml without api-keys", "config.yaml", "port: 9100\nremote-management:\n  secret-key: k\n"},
		{"yaml with empty access.api-keys", "config.yaml", "port: 9100\naccess:\n  api-keys: []\nmanagement:\n  secret-key: k\n"},
	} {
		if _, err := ParseConfig([]byte(c.cfg), c.path); err == nil || !strings.Contains(err.Error(), "client_keys is empty") {
			t.Errorf("%s: ParseConfig = %v, want the refusal of no client keys", c.name, err)
		}
	}
}

func TestConfigUsageKeepDays(t *testing.T) {
	const base = `"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"mgmt","extension":{"manifest":"m.json","accounts":[{"id":"a"}]}`
	for _, tc := range []struct {
		name, extra string
		want        int
		bad         bool
	}{
		{"absent defaults to 90", "", 90, false},
		{"zero keeps everything", `,"usage":{"keep_days":0}`, 0, false},
		{"positive", `,"usage":{"keep_days":30}`, 30, false},
		{"negative refused", `,"usage":{"keep_days":-1}`, 0, true},
		{"above max refused", `,"usage":{"keep_days":1000001}`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte("{"+base+tc.extra+"}"), "/tmp/config.json")
			if tc.bad {
				if err == nil {
					t.Fatal("accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Usage.KeepDaysValue(); got != tc.want {
				t.Errorf("KeepDaysValue() = %d, want %d", got, tc.want)
			}
		})
	}
}
