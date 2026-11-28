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

type Config struct {
	Listen                string                     `json:"listen"`
	ClientKeys            []string                   `json:"client_keys"`
	ClientKeyLabels       map[string]string          `json:"client_key_labels,omitempty"`
	Extension             ExtensionConfig            `json:"extension"`
	Extensions            map[string]ExtensionConfig `json:"extensions,omitempty"`
	Packages              PackagesConfig             `json:"packages"`
	Routing               route.Config               `json:"routing"`
	DropPolicy            string                     `json:"drop_policy,omitempty"`
	TokenCountEstimate    *bool                      `json:"token_count_estimate,omitempty"`
	ManagementKey         string                     `json:"management_key,omitempty"`
	ManagementAllowRemote bool                       `json:"management_allow_remote,omitempty"`
	CredentialsDir        string                     `json:"credentials_dir,omitempty"`
	ExtensionPages        ExtensionPagesConfig       `json:"extension_pages"`
	CORS                  CORSConfig                 `json:"cors"`
	TLS                   TLSFiles                   `json:"tls"`
	TrustedProxies        []string                   `json:"trusted_proxies,omitempty"`
	trusted               []netip.Prefix
	ProxyURL              string         `json:"proxy_url,omitempty"`
	Payload               *payload.Rules `json:"payload,omitempty"`
	keyDigests            [][sha256.Size]byte
	keyHash               func([]byte) [sha256.Size]byte
	managementDigest      *[sha256.Size]byte
	Usage                 UsageConfig `json:"usage"`
	Logs                  LogsConfig  `json:"logs"`
	Pprof                 PprofConfig `json:"pprof"`

	Notices []string             `json:"-"`
	AuthDir string               `json:"-"`
	Import  legacy.ImportOptions `json:"-"`
}

func (c *Config) prepareClientKeys() {
	c.keyDigests = make([][sha256.Size]byte, len(c.ClientKeys))
	for i, k := range c.ClientKeys {
		c.keyDigests[i] = c.digest([]byte(k))
	}
}

func (c *Config) digest(b []byte) [sha256.Size]byte {
	if c.keyHash != nil {
		return c.keyHash(b)
	}
	return sha256.Sum256(b)
}

func (c *Config) prepareManagementKey() {
	d := sha256.Sum256([]byte(c.ManagementKey))
	c.managementDigest = &d
}

func (c *Config) managementKeyDigest() [sha256.Size]byte {
	if c.managementDigest != nil {
		return *c.managementDigest
	}
	return sha256.Sum256([]byte(c.ManagementKey))
}

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

func (c *Config) TokenCountEstimateOn() bool {
	return c.TokenCountEstimate == nil || *c.TokenCountEstimate
}

type TLSFiles struct {
	Enable bool   `json:"enable,omitempty"`
	Cert   string `json:"cert,omitempty"`
	Key    string `json:"key,omitempty"`
}

type ExtensionPagesConfig struct {
	Listen string `json:"listen,omitempty"`
	URL    string `json:"url,omitempty"`
}

type UsageConfig struct {
	Enabled   *bool `json:"enabled,omitempty"`
	MaxEvents int   `json:"max_events,omitempty"`
	MaxAgeMS  int64 `json:"max_age_ms,omitempty"`
	KeepDays  *int  `json:"keep_days,omitempty"`
}

func (u UsageConfig) On() bool { return u.Enabled == nil || *u.Enabled }

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

type LogsConfig struct {
	ToFile            bool `json:"to_file,omitempty"`
	MaxTotalSizeMB    int  `json:"max_total_size_mb,omitempty"`
	RequestLog        bool `json:"request_log,omitempty"`
	ErrorLogsMaxFiles *int `json:"error_logs_max_files,omitempty"`
}

func (l LogsConfig) ErrorLogs() int {
	if l.ErrorLogsMaxFiles == nil {
		return 10
	}
	return *l.ErrorLogsMaxFiles
}

type PprofConfig struct {
	Enable bool   `json:"enable,omitempty"`
	Addr   string `json:"addr,omitempty"`
}

const defaultPprofAddr = "127.0.0.1:8316"

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

type PackagesConfig struct {
	Dir        string              `json:"dir,omitempty"`
	Registries []packages.Registry `json:"registries,omitempty"`
}

const BundledRegistry = "bundled"

type ExtensionConfig struct {
	Manifest       string             `json:"manifest,omitempty"`
	Accounts       []Account          `json:"accounts"`
	ID             string             `json:"-"`
	IdleStopMS     *int64             `json:"idle_stop_ms,omitempty"`
	DrainTimeoutMS *int64             `json:"drain_timeout_ms,omitempty"`
	Restart        RestartConfig      `json:"restart"`
	ModelAlias     []route.ModelAlias `json:"model_alias,omitempty"`
	ExcludedModels []string           `json:"excluded_models,omitempty"`
}

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

type RestartConfig struct {
	BackoffMS       int64 `json:"backoff_ms,omitempty"`
	MaxBackoffMS    int64 `json:"max_backoff_ms,omitempty"`
	QuarantineAfter int   `json:"quarantine_after,omitempty"`
	StableMS        int64 `json:"stable_ms,omitempty"`
}

type Account = route.Account

const maxTimingMS = 30 * 24 * 60 * 60 * 1000

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

func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(b, path)
}

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

func IsYAML(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml"
}

var ErrLabelIsKey = errors.New("a label must not be the same as a client key or the management key")

func (c *Config) LabelIsKey(label string) bool {
	return label != "" && (label == c.ManagementKey || slices.Contains(c.ClientKeys, label))
}

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
		if keys := unknownKeys(b, reflect.TypeOf(c)); len(keys) > 0 {
			c.Notices = append(c.Notices, unknownKeysNotice(path, keys))
		}
	}
	if c.Listen == "" {
		return nil, errors.New("config: listen is required")
	}
	if len(c.ClientKeys) == 0 {
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
		c.TLS.Cert, c.TLS.Key = strings.TrimSpace(c.TLS.Cert), strings.TrimSpace(c.TLS.Key)
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

func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// Go's dual-stack tcp listener accepts IPv4 loopback even when the OS reports [::].
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

func jsonConfigError(b []byte, err error) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn):
		off := int(syn.Offset) - 1
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

func lineColumn(b []byte, off int) (line, col int) {
	if len(b) == 0 {
		return 1, 1
	}
	off = max(0, min(off, len(b)-1))
	head := b[:off+1]
	line = 1 + bytes.Count(head, []byte("\n"))
	return line, utf8.RuneCount(head[bytes.LastIndexByte(head, '\n')+1:])
}

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

func closestKey(k string, fields map[string]reflect.StructField) string {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if editsAway(strings.ToLower(k), strings.ToLower(name)) {
			return " (did you mean " + strconv.Quote(name) + "?)"
		}
	}
	return ""
}

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

func unknownKeysNotice(path string, keys []string) string {
	return fmt.Sprintf("warning: config %s: unknown keys, ignored (check the spelling; core does not read them): %s", path, strings.Join(keys, ", "))
}
