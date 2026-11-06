// Package server is core's HTTP API: it serves client formats and sends each request to the
// provider extension.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
)

// Config is core's configuration file, version 0.
type Config struct {
	// Listen is the TCP address to serve on, for example 127.0.0.1:8400.
	Listen string `json:"listen"`
	// ClientKeys are the keys clients must send as a bearer token. Empty means no check.
	ClientKeys []string `json:"client_keys"`
	// Extension is the extension run from a manifest path, with its accounts. Optional once
	// packages are used.
	Extension ExtensionConfig `json:"extension"`
	// Extensions are the settings of installed packages, by package ID. They apply while the
	// package is enabled.
	Extensions map[string]ExtensionConfig `json:"extensions,omitempty"`
	// Packages says where installed packages are kept and where they are installed from.
	Packages PackagesConfig `json:"packages"`
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
	// ExtensionPages says where extension pages are served: on a site of their own, never the
	// one serving the panel (next/ui/extension-pages/README.md).
	ExtensionPages ExtensionPagesConfig `json:"extension_pages"`
}

// ExtensionPagesConfig is the extension_pages section of the config.
type ExtensionPagesConfig struct {
	// Listen is the pages listener's address. Default: the host of listen, port 0 (a free port
	// core picks at start).
	Listen string `json:"listen,omitempty"`
	// URL is the pages site's origin as browsers reach it. Default: derived for a panel on a
	// loopback name (the other loopback name, on the pages listener's port).
	URL string `json:"url,omitempty"`
}

// PackagesConfig is the packages section of the config.
type PackagesConfig struct {
	// Dir is the packages folder, relative to the config file. Default: "packages" beside it.
	Dir string `json:"dir,omitempty"`
	// Registries are searched in order when an install names none.
	Registries []packages.Registry `json:"registries,omitempty"`
}

// BundledRegistry is the name of the registry of the packages a pluxy install ships with.
const BundledRegistry = "bundled"

// ExtensionConfig is one extension's accounts and how its program is run.
type ExtensionConfig struct {
	// Manifest is the path of the extension's manifest, relative to the config file. Only for
	// the extension run from a path (Config.Extension).
	Manifest string    `json:"manifest,omitempty"`
	Accounts []Account `json:"accounts"`
	// ID is the extension's ID: the key in Config.Extensions; for Config.Extension, the
	// manifest's, set by core once it has read the manifest.
	ID string `json:"-"`
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

// validate checks the section called name; seen collects account IDs across sections.
func (x ExtensionConfig) validate(name string, seen map[string]bool) error {
	for i, a := range x.Accounts {
		if a.ID == "" {
			return fmt.Errorf("config: %s.accounts[%d].id is required", name, i)
		}
		if seen[a.ID] {
			return fmt.Errorf("config: %s.accounts[%d].id %q is used twice", name, i, a.ID)
		}
		seen[a.ID] = true
		if a.MaxConcurrency < 0 {
			return fmt.Errorf("config: %s.accounts[%d].max_concurrency must not be negative", name, i)
		}
	}
	for _, ms := range []*int64{x.IdleStopMS, x.DrainTimeoutMS, &x.Restart.BackoffMS, &x.Restart.MaxBackoffMS, &x.Restart.StableMS} {
		if ms != nil && (*ms < 0 || *ms > maxTimingMS) {
			return fmt.Errorf("config: %s timings must be between 0 and %d ms (30 days)", name, maxTimingMS)
		}
	}
	if x.Restart.QuarantineAfter < 0 {
		return fmt.Errorf("config: %s.restart.quarantine_after must not be negative", name)
	}
	return nil
}

// prepare checks the section and sets its defaults; listen is the config's listen.
func (p *ExtensionPagesConfig) prepare(listen string) error {
	if p.Listen == "" {
		host, _, err := net.SplitHostPort(listen)
		if err != nil {
			return fmt.Errorf("config: listen %q: %w", listen, err)
		}
		p.Listen = net.JoinHostPort(host, "0")
	} else if _, _, err := net.SplitHostPort(p.Listen); err != nil {
		return fmt.Errorf("config: extension_pages.listen %q: %w", p.Listen, err)
	}
	if p.URL == "" {
		return nil
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("config: extension_pages.url %q must be an origin: http:// or https://, a host and maybe a port, nothing else", p.URL)
	}
	p.URL = u.Scheme + "://" + strings.ToLower(u.Host)
	return nil
}

// Accounts returns every account the config sets, in every section, each with the ID of its
// section's extension (empty for Config.Extension until core has read its manifest).
func (c *Config) Accounts() []Account {
	var out []Account
	sections := []ExtensionConfig{c.Extension}
	for _, id := range slices.Sorted(maps.Keys(c.Extensions)) {
		sections = append(sections, c.Extensions[id])
	}
	for _, x := range sections {
		for _, a := range x.Accounts {
			a.Extension = x.ID
			out = append(out, a)
		}
	}
	return out
}

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
	if err := c.ExtensionPages.prepare(c.Listen); err != nil {
		return nil, err
	}
	base := filepath.Dir(path)
	if abs, err := filepath.Abs(base); err == nil {
		base = abs
	}
	rel := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	if c.Extension.Manifest == "" && len(c.Extension.Accounts) > 0 {
		return nil, errors.New("config: extension.accounts needs extension.manifest; accounts of an installed package go in extensions.<id>.accounts")
	}
	c.Extension.Manifest = rel(c.Extension.Manifest)
	if c.Packages.Dir == "" {
		c.Packages.Dir = "packages"
	}
	c.Packages.Dir = rel(c.Packages.Dir)
	names := map[string]bool{BundledRegistry: true}
	for i := range c.Packages.Registries {
		r := &c.Packages.Registries[i]
		if r.Name == "" || names[r.Name] {
			return nil, fmt.Errorf("config: packages.registries[%d].name must be set, unique and not %q", i, BundledRegistry)
		}
		names[r.Name] = true
		if strings.Contains(r.URL, "://") && !strings.HasPrefix(strings.ToLower(r.URL), "https://") {
			return nil, fmt.Errorf("config: packages.registries[%d].url: only https:// URLs and local paths are allowed", i)
		}
		if !strings.HasPrefix(strings.ToLower(r.URL), "https://") {
			r.URL = rel(r.URL)
		}
		if err := packages.CheckLocation(r.URL); err != nil {
			return nil, fmt.Errorf("config: packages.registries[%d]: %w", i, err)
		}
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
	seen := map[string]bool{}
	if err := c.Extension.validate("extension", seen); err != nil {
		return nil, err
	}
	for id, x := range c.Extensions {
		name := fmt.Sprintf("extensions[%q]", id)
		if !host.ValidID(id) {
			return nil, fmt.Errorf("config: %s: %q is not a package ID of the form publisher/name", name, id)
		}
		if x.Manifest != "" {
			return nil, fmt.Errorf("config: %s.manifest: extensions holds settings of installed packages; an extension run from a manifest path goes in extension", name)
		}
		if err := x.validate(name, seen); err != nil {
			return nil, err
		}
		x.ID = id
		c.Extensions[id] = x
	}
	if len(seen) == 0 && c.ManagementKey == "" {
		return nil, errors.New("config: set at least one account unless management_key is set (accounts can then be added by login)")
	}
	if err := c.Routing.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return &c, nil
}
