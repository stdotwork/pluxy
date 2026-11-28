// Package server is core's HTTP API: it serves client formats and sends each request to the
// provider extension.
package server

import (
	"bytes"
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/packages"
	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/upheader"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk/payload"
)

// Config is core's configuration file, version 0.
type Config struct {
	// Listen is the TCP address to serve on, for example 127.0.0.1:8400.
	Listen string `json:"listen"`
	// ClientKeys are the keys clients must send as a bearer token. At least one is required:
	// core serves no client without a key.
	ClientKeys []string `json:"client_keys"`
	// ClientKeyLabels are the names of client keys, by the ID the management API lists a key
	// under (never the key itself). A key without an entry has no name. Each label passes
	// CheckLabel and is not a client key or the management key; ParseConfig trims them and drops an
	// empty one.
	ClientKeyLabels map[string]string `json:"client_key_labels,omitempty"`
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
	// TokenCountEstimate says what a token count gets when it reaches an account that cannot
	// count tokens (its upstream has no count endpoint) and no account answers it: true, the
	// default, answers with core's own estimate (package tokenest); false answers with the
	// error. A count that only accounts that can count refuse is an error either way.
	TokenCountEstimate *bool `json:"token_count_estimate,omitempty"`
	// ManagementKey is the key of the management API (ManagementPrefix); clients send it as a
	// bearer token. Required: a config without one is refused.
	ManagementKey string `json:"management_key,omitempty"`
	// ManagementAllowRemote serves the management API to clients on other machines too (today's
	// management.allow-remote). Default: only to this machine (FromThisMachine).
	ManagementAllowRemote bool `json:"management_allow_remote,omitempty"`
	// CredentialsDir is the folder that keeps accounts added by login, relative to the config
	// file. Default: "credentials" beside the config file.
	CredentialsDir string               `json:"credentials_dir,omitempty"`
	ExtensionPages ExtensionPagesConfig `json:"extension_pages"`
	// CORS says which web pages may call the client API from a browser. Default: none.
	CORS CORSConfig `json:"cors"`
	// TLS serves listen over HTTPS (today's server.tls).
	TLS TLSFiles `json:"tls"`
	// TrustedProxies are the IP addresses and CIDR ranges of the proxies in front of core whose
	// X-Forwarded-For or X-Real-IP header names the client (today's server.trusted-proxies).
	// Default: none; a request comes from its connection's peer.
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	trusted        []netip.Prefix
	// ProxyURL is the proxy every upstream request of an extension goes through, for each
	// account without a proxy_url of its own (today's proxy-url): an http, https, socks5 or
	// socks5h URL, or direct or none for no proxy. Default: none. Its user name and password are
	// secret (package proxyurl).
	ProxyURL string `json:"proxy_url,omitempty"`
	// Payload is the payload rules (today's payload): fields each extension sets, fills in or
	// removes in the JSON bodies it sends upstream, for the requests the rules match. Default:
	// none.
	Payload *payload.Rules `json:"payload,omitempty"`
	// keyDigests are the SHA-256 digests of ClientKeys, made when the config is loaded so that a
	// request's check does not hash a key (prepareClientKeys).
	keyDigests [][sha256.Size]byte
	// keyHash, when set, is what digest hashes with instead of sha256.Sum256: tests set it to
	// count the bytes a request's key check hashes.
	keyHash func([]byte) [sha256.Size]byte
	// managementDigest is the SHA-256 digest of ManagementKey, made when the config is loaded for
	// the same reason (prepareManagementKey); nil: not made.
	managementDigest *[sha256.Size]byte
	// Usage says whether core keeps a usage record of the requests it serves, for the
	// management API, and how much of it.
	Usage UsageConfig `json:"usage"`
	// Logs says whether core also writes its log to files in its logs folder, the folder logs
	// beside the config file.
	Logs LogsConfig `json:"logs"`
	// Pprof is the profiling pages' section: they are served on a listener of their own, on
	// loopback only (today's observability.pprof). Off by default.
	Pprof PprofConfig `json:"pprof"`

	// From a config.yaml with today's core's key names (package legacy):
	// Notices name the settings it uses that core does not support or know; core logs them
	// at each load.
	Notices []string `json:"-"`
	// AuthDir is today's auth folder (oauth.auth-dir), absolute; empty when not set.
	AuthDir string `json:"-"`
	// Import says how accounts imported from today's auth folder are set up.
	Import legacy.ImportOptions `json:"-"`
}

// prepareClientKeys makes the digests of the client keys, which authorized compares a request's
// token with. Hashing the keys here, once, keeps the time of a request's check from depending on
// how long a key is, and from growing with the number and length of the keys.
func (c *Config) prepareClientKeys() {
	c.keyDigests = make([][sha256.Size]byte, len(c.ClientKeys))
	for i, k := range c.ClientKeys {
		c.keyDigests[i] = c.digest([]byte(k))
	}
}

// digest hashes b with the hash a test replaced sha256.Sum256 with, if it did (keyHash), and
// with sha256.Sum256 otherwise.
func (c *Config) digest(b []byte) [sha256.Size]byte {
	if c.keyHash != nil {
		return c.keyHash(b)
	}
	return sha256.Sum256(b)
}

// prepareManagementKey makes the digest of the management key, which the management API compares
// a request's key with, so a request's check does not hash the key.
func (c *Config) prepareManagementKey() {
	d := sha256.Sum256([]byte(c.ManagementKey))
	c.managementDigest = &d
}

// managementKeyDigest returns the digest of the management key: the one made at load, or, for a
// Config that was built by hand (tests), made now.
func (c *Config) managementKeyDigest() [sha256.Size]byte {
	if c.managementDigest != nil {
		return *c.managementDigest
	}
	return sha256.Sum256([]byte(c.ManagementKey))
}

// clientKeyDigests returns the digests of the client keys: the ones made at load, or, for a
// Config that was built by hand (tests), made now.
func (c *Config) clientKeyDigests() [][sha256.Size]byte {
	if len(c.keyDigests) == len(c.ClientKeys) {
		return c.keyDigests
	}
	out := make([][sha256.Size]byte, len(c.ClientKeys))
	for i, k := range c.ClientKeys {
		out[i] = c.digest([]byte(k))
	}
	return out
}

// TokenCountEstimateOn reports whether a token count no account can give is answered with an
// estimate (token_count_estimate; absent: on).
func (c *Config) TokenCountEstimateOn() bool {
	return c.TokenCountEstimate == nil || *c.TokenCountEstimate
}

// TLSFiles is the tls section of the config, with today's core's key names.
type TLSFiles struct {
	// Enable serves listen over HTTPS (HTTP/2 or HTTP/1.1) instead of plain HTTP.
	Enable bool `json:"enable,omitempty"`
	// Cert is the certificate file (PEM, the leaf first, then its chain) and Key its private
	// key file (PEM), relative to the config file. Both are required with Enable.
	Cert string `json:"cert,omitempty"`
	Key  string `json:"key,omitempty"`
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

// UsageConfig is the usage section of the config: the usage record (package usage).
type UsageConfig struct {
	// Enabled turns the usage record on (today's usage-statistics-enabled). Absent: on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxEvents is the most requests the record keeps; the oldest go first. Default 10000, at
	// most 200000.
	MaxEvents int `json:"max_events,omitempty"`
	// MaxAgeMS is how long the record keeps a request. Default 24 hours, at most 30 days.
	MaxAgeMS int64 `json:"max_age_ms,omitempty"`
	// KeepDays is how many UTC days of usage day files are kept; 0 keeps everything. Absent: 90.
	// It does not delete their summaries (usage/summary).
	KeepDays *int `json:"keep_days,omitempty"`
}

// On reports whether the usage record is on.
func (u UsageConfig) On() bool { return u.Enabled == nil || *u.Enabled }

// KeepDaysValue returns how many UTC days of usage files are kept; 0 keeps everything.
func (u UsageConfig) KeepDaysValue() int {
	if u.KeepDays == nil {
		return usage.DefaultKeepDays
	}
	return *u.KeepDays
}

func (u UsageConfig) validate() error {
	if u.MaxEvents < 0 || u.MaxEvents > usage.MaxMaxEvents {
		return fmt.Errorf("config: usage.max_events must be between 0 (the default, %d) and %d", usage.DefaultMaxEvents, usage.MaxMaxEvents)
	}
	if u.MaxAgeMS < 0 || u.MaxAgeMS > usage.MaxMaxAge.Milliseconds() {
		return fmt.Errorf("config: usage.max_age_ms must be between 0 (the default, 24 hours) and %d (30 days)", usage.MaxMaxAge.Milliseconds())
	}
	if u.KeepDays != nil && (*u.KeepDays < 0 || *u.KeepDays > usage.MaxKeepDays) {
		return fmt.Errorf("config: usage.keep_days must be between 0 (keep everything) and %d", usage.MaxKeepDays)
	}
	return nil
}

// LogsConfig is the logs section of the config.
type LogsConfig struct {
	// ToFile also writes core's log to logs/main.log, rotated at 10 MB (today's
	// logging-to-file).
	ToFile bool `json:"to_file,omitempty"`
	// MaxTotalSizeMB caps the logs folder's .log files: the oldest go first, main.log never
	// (today's logs-max-total-size-mb). 0: no cap.
	MaxTotalSizeMB int `json:"max_total_size_mb,omitempty"`
	// RequestLog writes every client request and its reply to a file in the logs folder
	// (today's request-log). Off, only the requests that fail are written, as error-*.log.
	RequestLog bool `json:"request_log,omitempty"`
	// ErrorLogsMaxFiles is how many error logs are kept, the newest (today's
	// error-logs-max-files). Absent: 10. 0: all.
	ErrorLogsMaxFiles *int `json:"error_logs_max_files,omitempty"`
}

// ErrorLogs returns how many error logs are kept; 0 is all.
func (l LogsConfig) ErrorLogs() int {
	if l.ErrorLogsMaxFiles == nil {
		return 10
	}
	return *l.ErrorLogsMaxFiles
}

// PprofConfig is the pprof section of the config.
type PprofConfig struct {
	// Enable serves Go's profiling pages (/debug/pprof/) on Addr. Only a build with the pprof
	// build tag has them; the default build says so in the log and serves nothing.
	Enable bool `json:"enable,omitempty"`
	// Addr is the profiling listener's address, on loopback only: 127.0.0.0/8, ::1 or localhost.
	// Default: 127.0.0.1:8316.
	Addr string `json:"addr,omitempty"`
}

// defaultPprofAddr is the profiling listener's address when the config names none.
const defaultPprofAddr = "127.0.0.1:8316"

// prepare sets the default address and checks it, also while Enable is off, so a bad address is
// refused before it is needed. The pages show the program's memory and goroutines, so they are
// reachable from this machine only.
func (p *PprofConfig) prepare() error {
	if p.Addr == "" {
		p.Addr = defaultPprofAddr
	}
	host, port, err := net.SplitHostPort(p.Addr)
	if err != nil {
		return fmt.Errorf("config: pprof.addr %q: %w", p.Addr, err)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("config: pprof.addr %q: the port must be a number from 0 to 65535", p.Addr)
	}
	if !IsLoopbackHost(host) {
		return fmt.Errorf("config: pprof.addr %q must be a loopback address (127.0.0.0/8, ::1 or localhost): the profiling pages show the program's memory and goroutines, so they are served on this machine only", p.Addr)
	}
	return nil
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
	// ModelAlias renames models for every account of the extension added by a sign-in (not an
	// API key), after the account's own model_alias (today's oauth-model-alias); see
	// route.ModelAlias.
	ModelAlias []route.ModelAlias `json:"model_alias,omitempty"`
	// ExcludedModels are model name patterns no account of the extension added by a sign-in (not
	// an API key) serves, unless the account has its own excluded_models (today's
	// oauth-excluded-models).
	ExcludedModels []string `json:"excluded_models,omitempty"`
}

// LoginDefaults returns account a, added by login to the extension x configures with a
// credential of kind, with x's model_alias after its own (an alias the account already has is
// not added twice) and, when it has no excluded_models of its own, x's. An account added with an
// API key gets neither, as today's oauth-model-alias and oauth-excluded-models skip API keys.
func (x ExtensionConfig) LoginDefaults(a Account, kind string) Account {
	if kind == protocol.CredentialAPIKey {
		return a
	}
	if len(a.ExcludedModels) == 0 {
		a.ExcludedModels = x.ExcludedModels
	}
	if len(x.ModelAlias) > 0 {
		aliases := slices.Clone(a.ModelAlias)
		for _, r := range x.ModelAlias {
			if !slices.ContainsFunc(aliases, func(o route.ModelAlias) bool { return strings.EqualFold(o.Alias, r.Alias) }) {
				aliases = append(aliases, r)
			}
		}
		a.ModelAlias = aliases
	}
	return a
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
		if a.RequestRetry != nil && *a.RequestRetry < 0 {
			return fmt.Errorf("config: %s.accounts[%d].request_retry must not be negative", name, i)
		}
		if err := proxyurl.Check(fmt.Sprintf("%s.accounts[%d].proxy_url", name, i), a.ProxyURL); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if err := upheader.Check(fmt.Sprintf("%s.accounts[%d].headers", name, i), a.Headers); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if err := route.ValidateTimeouts(fmt.Sprintf("%s.accounts[%d].timeouts", name, i), a.Timeouts); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if err := a.CheckNames(); err != nil {
			return fmt.Errorf("config: %s.accounts[%d]: %w", name, i, err)
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
	for i, r := range x.ModelAlias {
		if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Alias) == "" {
			return fmt.Errorf("config: %s.model_alias[%d] needs a name and an alias", name, i)
		}
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

// ExampleClientKeys returns the client keys that are the example config's, once each in the
// order the config lists them; today's core ignores spaces around a key when it checks.
func (c *Config) ExampleClientKeys() []string {
	var found []string
	for _, k := range c.ClientKeys {
		k = strings.TrimSpace(k)
		if slices.Contains(legacy.ExampleClientKeys, k) && !slices.Contains(found, k) {
			found = append(found, k)
		}
	}
	return found
}

// IsYAML reports whether the config file at path is a config.yaml with today's core's key
// names: its name ends in .yaml or .yml.
func IsYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

// ErrLabelIsKey is why a client key's label may not be a client key or the management key: the
// label is listed by the management API and recorded in usage events, which no key may reach.
var ErrLabelIsKey = errors.New("a label must not be the same as a client key or the management key")

// LabelIsKey reports whether label is one of the client keys or the management key.
func (c *Config) LabelIsKey(label string) bool {
	return label != "" && (label == c.ManagementKey || slices.Contains(c.ClientKeys, label))
}

// ParseConfig parses and validates the contents of the config file at path. Relative paths in
// it are relative to path's folder. A config.yaml (IsYAML) is read with today's core's key
// names (package legacy) and then checked as the JSON config is.
func ParseConfig(b []byte, path string) (*Config, error) {
	c := Config{Import: legacy.DefaultImportOptions}
	hashed := false
	if IsYAML(path) {
		res, err := legacy.Translate(b)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		if b, err = json.Marshal(res.Config); err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		c.Notices, c.AuthDir, c.Import = res.Notices, res.AuthDir, res.Import
		hashed = res.HashedManagementKey
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, jsonConfigError(b, err))
	}
	if !IsYAML(path) {
		// A config.yaml's unknown keys are noticed by package legacy.
		if keys := unknownKeys(b, reflect.TypeOf(c)); len(keys) > 0 {
			c.Notices = append(c.Notices, unknownKeysNotice(path, keys))
		}
	}
	if c.Listen == "" {
		return nil, errors.New("config: listen is required")
	}
	if len(c.ClientKeys) == 0 {
		// Today's core serves anyone without a key then; this core refuses to.
		return nil, errors.New("config: client_keys is empty (access.api-keys in a config.yaml): set at least one client key; core serves no client without one")
	}
	for id, label := range c.ClientKeyLabels {
		l, err := CheckLabel(label)
		if err != nil {
			return nil, fmt.Errorf("config %s: client_key_labels: the label of key %q: %w", path, id, err)
		}
		if l == "" {
			delete(c.ClientKeyLabels, id)
		} else {
			c.ClientKeyLabels[id] = l
		}
	}
	if c.ManagementKey == "" && hashed {
		return nil, fmt.Errorf("config %s: remote-management.secret-key is a bcrypt hash: replace it with a new key (pluxy cannot use a bcrypt hash); core always has a management key, so the panel and the pluxy command can manage it", path)
	}
	if c.ManagementKey == "" {
		// Today's core turned its management API and panel off without one, silently.
		where := `"management_key": "<a long random secret>"`
		if IsYAML(path) {
			where = "remote-management: {secret-key: <a long random secret>}"
		}
		return nil, fmt.Errorf("config %s: no management key: core always has one, so the panel and the pluxy command can manage it. Add %s to the file, or move the file aside and run pluxy init to write a new config with both keys", path, where)
	}
	for id, label := range c.ClientKeyLabels {
		if c.LabelIsKey(label) {
			return nil, fmt.Errorf("config %s: client_key_labels: the label of key %q: %w", path, id, ErrLabelIsKey)
		}
	}
	if ex := c.ExampleClientKeys(); len(ex) > 0 {
		// Core starts, as today's core does, so the management API stays open to fix the keys.
		c.Notices = append(c.Notices, "client_keys still holds the example config's public keys ("+strings.Join(ex, ", ")+
			"): every client request is refused (403 "+ExampleKeyCode+") until they are replaced")
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
	if err := c.CORS.prepare(); err != nil {
		return nil, err
	}
	if err := proxyurl.Check("proxy_url", c.ProxyURL); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if err := payload.Validate(c.Payload); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	trusted, err := parseTrustedProxies(c.TrustedProxies)
	if err != nil {
		return nil, err
	}
	c.trusted = trusted
	if c.TLS.Enable {
		c.TLS.Cert, c.TLS.Key = strings.TrimSpace(c.TLS.Cert), strings.TrimSpace(c.TLS.Key) // as today's core reads them
		if c.TLS.Cert == "" || c.TLS.Key == "" {
			return nil, errors.New("config: tls.enable needs tls.cert and tls.key")
		}
		c.TLS.Cert, c.TLS.Key = rel(c.TLS.Cert), rel(c.TLS.Key)
	}
	if err := c.Usage.validate(); err != nil {
		return nil, err
	}
	if err := c.Pprof.prepare(); err != nil {
		return nil, err
	}
	if c.Logs.MaxTotalSizeMB < 0 || c.Logs.MaxTotalSizeMB > 1<<20 {
		return nil, errors.New("config: logs.max_total_size_mb must be between 0 (no cap) and 1048576")
	}
	if n := c.Logs.ErrorLogsMaxFiles; n != nil && (*n < 0 || *n > 1000000) {
		return nil, errors.New("config: logs.error_logs_max_files must be between 0 (keep all) and 1000000")
	}
	switch c.DropPolicy {
	case "", llm.DropReport, llm.DropReject:
	default:
		return nil, fmt.Errorf("config: drop_policy must be %q or %q", llm.DropReport, llm.DropReject)
	}
	if c.AuthDir != "" {
		// As today's core reads it, ~ is the home folder.
		dir, err := ExpandHome(c.AuthDir)
		if err != nil {
			return nil, fmt.Errorf("config: oauth.auth-dir %q: %w", c.AuthDir, err)
		}
		c.AuthDir = rel(dir)
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
	if err := c.Routing.Validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.prepareClientKeys()
	c.prepareManagementKey()
	return &c, nil
}

// ExpandHome returns path with a leading ~ (alone, or before a path separator) replaced by the
// home folder.
func ExpandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok || rest != "" && rest[0] != '/' && rest[0] != filepath.Separator {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// IsLoopbackHost reports whether host (a name or an address, with no port) reaches this machine
// only: localhost or a loopback address.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// LocalURL is the address a program on this machine reaches a server listening on listen at:
// a listen address with no host or an unspecified one (0.0.0.0 or ::, every interface) is
// reached at 127.0.0.1 (Go's dual-stack tcp listener accepts IPv4 loopback even when the OS
// reports [::]). Specific addresses, including [::1], stay as written.
func LocalURL(listen string, tlsEnable bool) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	scheme := "http"
	if tlsEnable {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// ReachedURL is the address a request reached core by: its Host, with https when the connection
// is TLS. A caller on another machine uses that address, since core's own listen address names
// the wrong machine for it. A Host that is not a host and port is refused.
func ReachedURL(r *http.Request) (string, error) {
	u, err := url.Parse("//" + r.Host)
	if err != nil || r.Host == "" || u.Host != r.Host || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("the request's Host %q is not an address core can name", r.Host)
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host, nil
}

// jsonConfigError says what is wrong with a JSON config in the file's own terms: where it is
// (the key path as written, the line and column of a syntax error), what was expected and what
// was found, with no Go type names and no value from the file (it may be a secret). Any other
// error is returned as it is.
func jsonConfigError(b []byte, err error) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		off := int(syn.Offset) - 1 // the offset is after the byte that broke the syntax
		if syn.Error() == "unexpected end of JSON input" {
			off = len(bytes.TrimRight(b, " \t\r\n")) - 1
		}
		line, col := lineColumn(b, off)
		return fmt.Errorf("not valid JSON at line %d, column %d: %s", line, col, syn)
	case errors.As(err, &typ) && typ.Type != nil:
		line, _ := lineColumn(b, int(typ.Offset)-1)
		where := "the file"
		if typ.Field != "" {
			where = typ.Field
		}
		return fmt.Errorf("%s (line %d): expected %s, found %s", where, line, jsonWant(typ.Type), jsonFound(typ.Value))
	}
	return err
}

// lineColumn is the line and column, both counted from 1, of byte off of b.
func lineColumn(b []byte, off int) (line, col int) {
	if len(b) == 0 {
		return 1, 1
	}
	off = max(0, min(off, len(b)-1))
	head := b[:off+1]
	line = 1 + bytes.Count(head, []byte("\n"))
	return line, utf8.RuneCount(head[bytes.LastIndexByte(head, '\n')+1:])
}

// jsonWant names a JSON value of the kind of t, which may be a pointer.
func jsonWant(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Struct, reflect.Map:
		return "an object"
	}
	return "a different kind of value"
}

// jsonFound names the kind of JSON value the decoder reports in an UnmarshalTypeError.
func jsonFound(v string) string {
	switch {
	case v == "string", v == "number":
		return "a " + v
	case v == "array":
		return "a list"
	case v == "object":
		return "an object"
	case v == "bool":
		return "true or false"
	case strings.HasPrefix(v, "number "):
		return "a number that is not a whole number or is out of range"
	}
	return v
}

// unknownKeys returns the path of every key of the JSON document b that type t does not
// read, in the order the file has them, matched as encoding/json matches a key to a field. A
// map's keys are the user's own and never listed, nor are the keys of a value that keeps its
// own decoding (an extension's settings, a model alias). A document that does not fit t has
// none: the decoder has said so already.
func unknownKeys(b []byte, t reflect.Type) []string {
	var doc any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&doc) != nil {
		return nil
	}
	return walkKeys(doc, t, "", nil)
}

var (
	unmarshalerType     = reflect.TypeFor[json.Unmarshaler]()
	textUnmarshalerType = reflect.TypeFor[encoding.TextUnmarshaler]()
)

func walkKeys(v any, t reflect.Type, path string, out []string) []string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if reflect.PointerTo(t).Implements(unmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return out
	}
	switch v := v.(type) {
	case map[string]any:
		switch t.Kind() {
		case reflect.Map:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				out = walkKeys(v[k], t.Elem(), path+"["+strconv.Quote(k)+"]", out)
			}
		case reflect.Struct:
			fields := jsonFields(t)
			for _, k := range slices.Sorted(maps.Keys(v)) {
				sub := k
				if path != "" {
					sub = path + "." + k
				}
				f, ok := fields[k]
				if !ok {
					for name, g := range fields {
						if strings.EqualFold(name, k) {
							f, ok = g, true
						}
					}
				}
				if !ok {
					if strings.ContainsFunc(k, func(r rune) bool { return r <= ' ' || r == ',' || !strconv.IsPrint(r) }) {
						// A key that is not plain text is quoted, so it cannot break the log line.
						sub = strings.TrimSuffix(sub, k) + strconv.Quote(k)
					}
					sub += closestKey(k, fields)
					out = append(out, sub)
					continue
				}
				out = walkKeys(v[k], f.Type, sub, out)
			}
		}
	case []any:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			for i, e := range v {
				out = walkKeys(e, t.Elem(), path+"["+strconv.Itoa(i)+"]", out)
			}
		}
	}
	return out
}

// jsonFields maps the key of each field of struct type t that encoding/json reads to the field,
// including those of embedded structs.
func jsonFields(t reflect.Type) map[string]reflect.StructField {
	fields := map[string]reflect.StructField{}
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-" && f.Tag.Get("json") == "-":
		case f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct:
			for k, g := range jsonFields(f.Type) {
				fields[k] = g
			}
		case f.IsExported():
			if name == "" {
				name = f.Name
			}
			fields[name] = f
		}
	}
	return fields
}

// closestKey is " (did you mean "x"?)" for the known key x that k is one edit away from or
// that differs from it only in the letters' order, else "".
func closestKey(k string, fields map[string]reflect.StructField) string {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if editsAway(strings.ToLower(k), strings.ToLower(name)) {
			return " (did you mean " + strconv.Quote(name) + "?)"
		}
	}
	return ""
}

// editsAway reports whether a and b differ by one letter added, dropped or changed, or by two
// letters side by side swapped.
func editsAway(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	switch {
	case a == b:
		return false
	case len(b)-len(a) > 1:
		return false
	case len(a) == len(b):
		return a[i+1:] == b[i+1:] || (i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:])
	}
	return a[i:] == b[i+1:]
}

// unknownKeysNotice is the line core logs when a config file has keys it does not read: it
// names the file and the keys, never a value.
func unknownKeysNotice(path string, keys []string) string {
	return fmt.Sprintf("warning: config %s: unknown keys, ignored (check the spelling; core does not read them): %s", path, strings.Join(keys, ", "))
}
