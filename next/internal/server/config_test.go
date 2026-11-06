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
