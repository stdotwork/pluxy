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
	"github.com/stdotwork/pluxy/next/llm"
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
	// DropPolicy says what happens when translating a request to the upstream's format would
	// drop part of it: "report" (the default) sends it and logs what was dropped, "reject"
	// refuses it with an error naming what could not be carried.
	DropPolicy string `json:"drop_policy,omitempty"`
	// ManagementKey turns on the management API (ManagementPrefix); clients send it as a
	// bearer token. Empty: no management API.
	ManagementKey string `json:"management_key,omitempty"`
	// CredentialsDir is the folder that keeps accounts added by login, relative to the config
	// file. Default: "credentials" beside the config file.
	CredentialsDir string `json:"credentials_dir,omitempty"`
}

// ExtensionConfig names the provider extension and its accounts, and how its program is run.
type ExtensionConfig struct {
	// Manifest is the path of the extension's manifest, relative to the config file.
	Manifest string    `json:"manifest"`
	Accounts []Account `json:"accounts"`
	// IdleStopMS stops the program after this long without requests; the next request starts
	// it again. Absent: 10 minutes. 0: keep it running.
	IdleStopMS *int64 `json:"idle_stop_ms,omitempty"`
	// DrainTimeoutMS bounds how long a replaced version of the extension may finish the
	// requests that started on it. Absent: 10 minutes.
	DrainTimeoutMS *int64 `json:"drain_timeout_ms,omitempty"`
	// Restart says how a crashed program is restarted.
	Restart RestartConfig `json:"restart"`
}

// RestartConfig says how a crashed extension program is restarted. Zero fields take defaults.
type RestartConfig struct {
	// BackoffMS is the wait before the first restart; it doubles with each crash in a row.
	// Default 1000.
	BackoffMS int64 `json:"backoff_ms,omitempty"`
	// MaxBackoffMS caps the wait. Default 60000.
	MaxBackoffMS int64 `json:"max_backoff_ms,omitempty"`
	// QuarantineAfter is how many crashes in a row stop the restarts until the next reload.
	// Default 5.
	QuarantineAfter int `json:"quarantine_after,omitempty"`
	// StableMS: a program that ran this long before crashing starts a new count. Default
	// 600000.
	StableMS int64 `json:"stable_ms,omitempty"`
}

// Account is one upstream account served by the extension.
type Account = route.Account

// maxTimingMS bounds the extension timings: 30 days, far beyond any sensible value and far
// below what a time.Duration holds, even doubled.
const maxTimingMS = 30 * 24 * 60 * 60 * 1000

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(b, path)
}

// ParseConfig parses and validates the contents of the config file at path. Relative paths in
// it are relative to path's folder.
func ParseConfig(b []byte, path string) (*Config, error) {
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
	switch c.DropPolicy {
	case "", llm.DropReport, llm.DropReject:
	default:
		return nil, fmt.Errorf("config: drop_policy must be %q or %q", llm.DropReport, llm.DropReject)
	}
	if c.CredentialsDir == "" {
		c.CredentialsDir = "credentials"
	}
	if !filepath.IsAbs(c.CredentialsDir) {
		c.CredentialsDir = filepath.Join(filepath.Dir(path), c.CredentialsDir)
	}
	if len(c.Extension.Accounts) == 0 && c.ManagementKey == "" {
		return nil, errors.New("config: extension.accounts must not be empty unless management_key is set (accounts can then be added by login)")
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
	x := c.Extension
	for _, ms := range []*int64{x.IdleStopMS, x.DrainTimeoutMS, &x.Restart.BackoffMS, &x.Restart.MaxBackoffMS, &x.Restart.StableMS} {
		if ms != nil && (*ms < 0 || *ms > maxTimingMS) {
			return nil, fmt.Errorf("config: extension timings must be between 0 and %d ms (30 days)", maxTimingMS)
		}
	}
	if x.Restart.QuarantineAfter < 0 {
		return nil, errors.New("config: extension.restart.quarantine_after must not be negative")
	}
	if err := c.Routing.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}
