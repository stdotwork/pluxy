package server

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

func TestConfigRefusesHugeTimings(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a"}],"restart":{"stable_ms":9223372036855}}}`
	if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); err == nil || !strings.Contains(err.Error(), "timings") {
		t.Errorf("ParseConfig = %v, want an error about the timings", err)
	}
}

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
	if r := c.Routing; r.MaxWaitMS != 0 || r.RequestRetry == nil || *r.RequestRetry != 0 {
		t.Errorf("routing %+v, want today's defaults: request_retry 0, max_wait_ms 0", r)
	}

	c, err = ParseConfig([]byte("auth-dir: auths\napi-keys: [k]\nremote-management: {secret-key: m}\n"), "/srv/pluxy/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != "/srv/pluxy/auths" {
		t.Errorf("auth dir %q", c.AuthDir)
	}
	if _, err := ParseConfig([]byte("auth-dir: auths\napi-keys: [k]\n"), "/srv/pluxy/config.yaml"); err == nil || !strings.Contains(err.Error(), "remote-management: {secret-key:") {
		t.Errorf("a config.yaml with no management key: %v, want a refusal saying how to add one", err)
	}
	if _, err := ParseConfig([]byte("port: 9100\napi-keys: [k]\nremote-management: {secret-key: m}\n"), "/srv/pluxy/config.yaml"); err != nil {
		t.Errorf("a config.yaml with a management key and no account: %v", err)
	}
	if _, err := ParseConfig([]byte("port: 9100\n"), "/srv/pluxy/config.json"); err == nil {
		t.Error("a config.json in YAML was accepted")
	}
}

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

// The invalid JSON text +Inf or -Inf, which no upstream accepts: this core leaves the field out.
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

func TestConfigRoutingTimeouts(t *testing.T) {
	okCfg := `{
		"listen":"127.0.0.1:0",
		"client_keys":["k"],"management_key":"m",
		"routing":{"timeouts":{"header_ms":1500,"idle_ms":0,"total_ms":12000}},
		"extension":{"manifest":"m.json","accounts":[{"id":"a","timeouts":{"header_ms":0,"idle_ms":500}}]}
	}`
	c, err := ParseConfig([]byte(okCfg), "/tmp/config.json")
	if err != nil {
		t.Fatalf("ParseConfig = %v, want nil", err)
	}
	if c.Routing.Timeouts == nil || *c.Routing.Timeouts.HeaderMS != 1500 || *c.Routing.Timeouts.IdleMS != 0 || *c.Routing.Timeouts.TotalMS != 12000 {
		t.Fatalf("routing.timeouts = %+v", c.Routing.Timeouts)
	}
	ac := c.Accounts()[0].Timeouts
	if ac == nil || *ac.HeaderMS != 0 || *ac.IdleMS != 500 || ac.TotalMS != nil {
		t.Fatalf("account.timeouts = %+v", ac)
	}

	for _, bad := range []struct{ name, cfg string }{
		{"negative global header_ms", `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","routing":{"timeouts":{"header_ms":-1}},"extension":{"manifest":"m.json","accounts":[{"id":"a"}]}}`},
		{"negative global idle_ms", `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","routing":{"timeouts":{"idle_ms":-5}},"extension":{"manifest":"m.json","accounts":[{"id":"a"}]}}`},
		{"huge global total_ms", `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","routing":{"timeouts":{"total_ms":9223372036855}},"extension":{"manifest":"m.json","accounts":[{"id":"a"}]}}`},
		{"negative account idle_ms", `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a","timeouts":{"idle_ms":-1}}]}}`},
		{"huge account header_ms", `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a","timeouts":{"header_ms":9223372036855}}]}}`},
	} {
		if _, err := ParseConfig([]byte(bad.cfg), "/tmp/config.json"); err == nil {
			t.Errorf("%s: ParseConfig succeeded, want error", bad.name)
		}
	}
}

func TestConfigUsageKeepDays(t *testing.T) {
	const base = `"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a"}]}`
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

func TestConfigWarnsAboutUnknownKeys(t *testing.T) {
	const secret = "sk-very-secret-value-1234"
	cfg := `{"listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","bogus_setting":"` + secret + `","two\nlines":1,
	 "routing":{"explian":true,"chains":{"my-chain":[{"model":"m","provider":"pluxy/x"}]}},
	 "extension":{"manifest":"m.json","accounts":[{"id":"a","bogus_account_key":1,"headers":{"X-Anything":"` + secret + `"},"settings":{"free_form":{"inner":1}}}]},
	 "extensions":{"pluxy/other":{"accounts":[{"id":"b","priorty":1}]}}}`
	c, err := ParseConfig([]byte(cfg), "/tmp/config.json")
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	for _, n := range c.Notices {
		if strings.Contains(n, "unknown keys") {
			warns = append(warns, n)
		}
	}
	if len(warns) != 1 || !strings.HasPrefix(warns[0], "warning: ") || !strings.Contains(warns[0], "/tmp/config.json") {
		t.Fatalf("notices = %q, want one warning that names the file", c.Notices)
	}
	w := warns[0]
	for _, want := range []string{"bogus_setting", "routing.explian", "extension.accounts[0].bogus_account_key", `extensions["pluxy/other"].accounts[0].priorty`} {
		if strings.Count(w, want) != 1 {
			t.Errorf("warning %q names %q %d times, want once", w, want, strings.Count(w, want))
		}
	}
	for _, never := range []string{secret, "X-Anything", "my-chain", "free_form", "inner"} {
		if strings.Contains(w, never) {
			t.Errorf("warning %q holds %q, which is the user's own key or a value", w, never)
		}
	}
	if strings.ContainsAny(w, "\n\r") || !strings.Contains(w, `"two\nlines"`) {
		t.Errorf("warning %q does not quote a key that is not plain text", w)
	}
	if !strings.Contains(w, `priorty (did you mean "priority"?)`) {
		t.Errorf("warning %q does not offer the known key a typo is one edit from", w)
	}
}

func TestConfigKnownKeysGetNoWarning(t *testing.T) {
	cfg := `{"Listen":"127.0.0.1:0","client_keys":["k"],"management_key":"m","extension":{"manifest":"m.json","accounts":[{"id":"a","models":["x",{"name":"y","alias":"z"}]}]}}`
	c, err := ParseConfig([]byte(cfg), "/tmp/config.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Notices) != 0 {
		t.Errorf("notices = %q, want none", c.Notices)
	}
}

func TestConfigJSONErrorsInTheFilesTerms(t *testing.T) {
	for _, c := range []struct {
		name, cfg string
		want      []string
	}{
		{"type", `{"listen": 8400, "client_keys":["k"]}`,
			[]string{"config /tmp/config.json:", "listen", "expected a string, found a number", "line 1"}},
		{"nested type", "{\n\"client_keys\":[\"k\"],\n\"extension\":{\"accounts\":[{\"id\":\"a\",\"priority\":\"high\"}]}}",
			[]string{"extension.accounts.priority", "expected a whole number, found a string", "line 3"}},
		{"top level", `[1]`, []string{"the file", "expected an object, found a list"}},
		{"syntax", "{\n  \"listen\": \"x\",\n  \"client_keys\": [\"k\"],\n}", []string{"not valid JSON at line 4, column 1"}},
		{"truncated", "{\n  \"listen\": \"x\"\n", []string{"not valid JSON at line 2"}},
		{"empty", "", []string{"not valid JSON at line 1, column 1"}},
		{"blank", " \n", []string{"not valid JSON at line 1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(c.cfg), "/tmp/config.json")
			if err == nil {
				t.Fatal("ParseConfig = nil, want an error")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			for _, never := range []string{"Go struct", "Go value", "server.Config", "route.", "unmarshal"} {
				if strings.Contains(err.Error(), never) {
					t.Errorf("error %q holds %q", err, never)
				}
			}
		})
	}
}

func TestParseConfigRefusesKeyAsLabel(t *testing.T) {
	for _, secret := range []string{"key-a", "key-m", "  key-b "} {
		json := `{"listen": ":1", "management_key": "key-m", "client_keys": ["key-a", "key-b"], "client_key_labels": {"id-a": "` + secret + `"}}`
		yaml := "server: {host: 127.0.0.1, port: 9100}\naccess:\n  api-keys: [key-a, key-b]\nmanagement: {secret-key: key-m}\nclient_key_labels: {id-a: '" + secret + "'}\n"
		for name, tc := range map[string]struct{ src, path string }{"json": {json, "/srv/config.json"}, "yaml": {yaml, "/srv/config.yaml"}} {
			_, err := ParseConfig([]byte(tc.src), tc.path)
			if !errors.Is(err, ErrLabelIsKey) {
				t.Errorf("%s: label %q: err = %v, want ErrLabelIsKey", name, secret, err)
			} else if strings.Contains(err.Error(), "key-") {
				t.Errorf("%s: the error shows a key: %v", name, err)
			}
		}
	}
}

func TestParseConfigClientKeyLabels(t *testing.T) {
	json := `{"listen": ":1", "management_key": "m", "client_keys": ["a", "b"], "client_key_labels": {"id-a": "  Claude Code ", "id-b": "  "}}`
	yaml := "server: {host: 127.0.0.1, port: 9100}\naccess:\n  api-keys: [a, b]\nmanagement: {secret-key: m}\nclient_key_labels: {id-a: '  Claude Code ', id-b: ''}\n"
	for name, tc := range map[string]struct{ src, path string }{"json": {json, "/srv/config.json"}, "yaml": {yaml, "/srv/config.yaml"}} {
		c, err := ParseConfig([]byte(tc.src), tc.path)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := map[string]string{"id-a": "Claude Code"}; !maps.Equal(c.ClientKeyLabels, want) || !slices.Equal(c.ClientKeys, []string{"a", "b"}) {
			t.Errorf("%s: labels %q, keys %q; want %q and the plain keys", name, c.ClientKeyLabels, c.ClientKeys, want)
		}
		if len(c.Notices) != 0 {
			t.Errorf("%s: notices %q", name, c.Notices)
		}
	}
	c, err := ParseConfig([]byte(`{"listen": ":1", "management_key": "m", "client_keys": ["a"]}`), "/srv/config.json")
	if err != nil || len(c.ClientKeyLabels) != 0 {
		t.Errorf("a config without labels: %v, %q", err, c.ClientKeyLabels)
	}
	for _, label := range []string{strings.Repeat("x", 65), "a\\nb", "a\\u2028b"} {
		_, err := ParseConfig([]byte(`{"listen": ":1", "management_key": "m", "client_keys": ["a"], "client_key_labels": {"id-a": "`+label+`"}}`), "/srv/config.json")
		if err == nil || !strings.Contains(err.Error(), "client_key_labels") {
			t.Errorf("label %q: err = %v", label, err)
		}
	}
}

func TestLocalURL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		listen string
		tls    bool
		want   string
	}{
		{"unspecified IPv4", "0.0.0.0:18417", false, "http://127.0.0.1:18417"},
		{"unspecified IPv6", "[::]:18417", false, "http://127.0.0.1:18417"},
		{"empty host", ":18417", false, "http://127.0.0.1:18417"},
		{"unspecified IPv4 over https", "0.0.0.0:8443", true, "https://127.0.0.1:8443"},
		{"unspecified IPv6 over https", "[::]:8443", true, "https://127.0.0.1:8443"},
		{"specific IPv4 loopback", "127.0.0.1:8317", false, "http://127.0.0.1:8317"},
		{"specific IPv6 loopback", "[::1]:8317", false, "http://[::1]:8317"},
		{"specific LAN address", "10.0.0.2:18417", false, "http://10.0.0.2:18417"},
		{"hostname", "localhost:8317", false, "http://localhost:8317"},
	} {
		got, err := LocalURL(tc.listen, tc.tls)
		if err != nil {
			t.Errorf("%s: LocalURL(%q, %v) error: %v", tc.name, tc.listen, tc.tls, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: LocalURL(%q, %v) = %q, want %q", tc.name, tc.listen, tc.tls, got, tc.want)
		}
	}
}
