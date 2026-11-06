// Package server is core's HTTP API: it serves client formats and sends each request to the
// provider extension.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stdotwork/pluxy/next/internal/route"
)

// Config is core's configuration file, version 0.
type Config struct {
	// Listen is the TCP address to serve on, for example 127.0.0.1:8400.
	Listen string `json:"listen"`
	// ClientKeys are the keys clients must send as a bearer token. Empty means no check.
	ClientKeys []string        `json:"client_keys"`
	Extension  ExtensionConfig `json:"extension"`
	// Routing says how requests are spread over the accounts and retried.
	Routing route.Config `json:"routing"`
}

// ExtensionConfig names the provider extension and its accounts.
type ExtensionConfig struct {
	// Manifest is the path of the extension's manifest, relative to the config file.
	Manifest string    `json:"manifest"`
	Accounts []Account `json:"accounts"`
}

// Account is one upstream account served by the extension.
type Account = route.Account

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if c.Listen == "" {
		return nil, errors.New("config: listen is required")
	}
	if c.Extension.Manifest == "" {
		return nil, errors.New("config: extension.manifest is required")
	}
	if !filepath.IsAbs(c.Extension.Manifest) {
		c.Extension.Manifest = filepath.Join(filepath.Dir(path), c.Extension.Manifest)
	}
	if len(c.Extension.Accounts) == 0 {
		return nil, errors.New("config: extension.accounts must not be empty")
	}
	seen := map[string]bool{}
	for i, a := range c.Extension.Accounts {
		if a.ID == "" {
			return nil, fmt.Errorf("config: extension.accounts[%d].id is required", i)
		}
		if seen[a.ID] {
			return nil, fmt.Errorf("config: extension.accounts[%d].id %q is used twice", i, a.ID)
		}
		seen[a.ID] = true
		if a.MaxConcurrency < 0 {
			return nil, fmt.Errorf("config: extension.accounts[%d].max_concurrency must not be negative", i)
		}
	}
	if err := c.Routing.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}
