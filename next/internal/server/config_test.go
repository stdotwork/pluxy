package server

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/protocol"
)

// An extension timing too large for a time.Duration is refused, not wrapped into a negative
// duration.
func TestConfigRefusesHugeTimings(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","client_keys":["k"],"extension":{"manifest":"m.json","accounts":[{"id":"a"}],"restart":{"stable_ms":9223372036855}}}`
	if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); err == nil || !strings.Contains(err.Error(), "timings") {
		t.Errorf("ParseConfig = %v, want an error about the timings", err)
	}
}

// token_count_estimate is on unless the config turns it off, in a JSON config and, under the same
// name, in a config.yaml. A value that is not a boolean is refused.
func TestConfigTokenCountEstimate(t *testing.T) {
	const base = `"listen":"127.0.0.1:0","client_keys":["k"],"extension":{"manifest":"m.json","accounts":[{"id":"a"}]}`
	for _, c := range []struct {
		name, file, cfg string
		want, bad       bool
	}{
		{"absent", "config.json", `{` + base + `}`, true, false},
		{"on", "config.json", `{` + base + `,"token_count_estimate":true}`, true, false},
		{"off", "config.json", `{` + base + `,"token_count_estimate":false}`, false, false},
		{"off in a config.yaml", "config.yaml", "api-keys: [k]\nclaude-api-key: [{api-key: x}]\ntoken_count_estimate: false\n", false, false},
		{"absent in a config.yaml", "config.yaml", "api-keys: [k]\nclaude-api-key: [{api-key: x}]\n", true, false},
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

	// A relative auth-dir is relative to the config file; with it, the config needs no
	// account and no management key.
	c, err = ParseConfig([]byte("auth-dir: auths\napi-keys: [k]\n"), "/srv/pluxy/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != "/srv/pluxy/auths" {
		t.Errorf("auth dir %q", c.AuthDir)
	}
	// Without it, the usual check applies.
	if _, err := ParseConfig([]byte("port: 9100\napi-keys: [k]\n"), "/srv/pluxy/config.yaml"); err == nil {
		t.Error("a config.yaml with no account, no management key and no auth-dir was accepted")
	}
	// A JSON config is not read as YAML.
	if _, err := ParseConfig([]byte("port: 9100\n"), "/srv/pluxy/config.json"); err == nil {
		t.Error("a config.json in YAML was accepted")
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
