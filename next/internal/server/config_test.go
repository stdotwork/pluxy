package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An extension timing too large for a time.Duration is refused, not wrapped into a negative
// duration.
func TestConfigRefusesHugeTimings(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","extension":{"manifest":"m.json","accounts":[{"id":"a"}],"restart":{"stable_ms":9223372036855}}}`
	if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); err == nil || !strings.Contains(err.Error(), "timings") {
		t.Errorf("ParseConfig = %v, want an error about the timings", err)
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
		cfg := `{"listen":"127.0.0.1:0","management_key":"k","packages":{"registries":[{"name":"r","url":"` + url + `"}]}}`
		if _, err := ParseConfig([]byte(cfg), "/tmp/config.json"); (err == nil) != ok {
			t.Errorf("registry %q: ParseConfig = %v", url, err)
		}
	}
}

// A registry path is relative to the config file even when the config file's own path is
// relative, as the default "config.json" is.
func TestConfigRegistryPathRelativeToRelativeConfig(t *testing.T) {
	cfg := `{"listen":"127.0.0.1:0","management_key":"k","packages":{"registries":[{"name":"r","url":"registry/index.json"}]}}`
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
	if c.Routing.MaxWaitMS != 30000 {
		t.Errorf("max_wait_ms = %d, want today's default of 30 s", c.Routing.MaxWaitMS)
	}

	// A relative auth-dir is relative to the config file; with it, the config needs no
	// account and no management key.
	c, err = ParseConfig([]byte("auth-dir: auths\n"), "/srv/pluxy/config.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthDir != "/srv/pluxy/auths" {
		t.Errorf("auth dir %q", c.AuthDir)
	}
	// Without it, the usual check applies.
	if _, err := ParseConfig([]byte("port: 9100\n"), "/srv/pluxy/config.yaml"); err == nil {
		t.Error("a config.yaml with no account, no management key and no auth-dir was accepted")
	}
	// A JSON config is not read as YAML.
	if _, err := ParseConfig([]byte("port: 9100\n"), "/srv/pluxy/config.json"); err == nil {
		t.Error("a config.json in YAML was accepted")
	}
}
