// Package legacy reads what today's core (core/, the CLIProxyAPI fork) leaves behind: its
// config.yaml, with its key names in either layout it reads (v8 and the older spellings), and
// the credential files in its auth folder. It turns them into this core's config and
// credential records. The mapping is documented in next/README.md, "config.yaml with today's
// key names".
package legacy

import (
	"cmp"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Result is a config.yaml turned into this core's config.
type Result struct {
	// Config is this core's config file, as the JSON object the JSON config holds.
	Config map[string]any
	// Notices name the settings the file uses that this core does not support yet, ignores
	// for good, or does not know; core logs them at each load.
	Notices []string
	// AuthDir is today's auth folder (oauth.auth-dir), as written; empty when not set.
	AuthDir string
	// Import says how accounts imported from AuthDir are set up.
	Import ImportOptions
}

// ImportOptions are the settings of a config.yaml that apply to accounts imported from today's
// auth folder.
type ImportOptions struct {
	// CodexIdentity is false when upstream.codex.disable-codex-cloaking is set: imported Codex
	// subscription accounts then get "codex_identity": false.
	CodexIdentity bool `json:"codex_identity"`
	// ImageGenerationTool is false when multimedia.disable-image-generation turns off the
	// hosted image_generation tool: imported Codex accounts then get
	// "image_generation_tool": false.
	ImageGenerationTool bool `json:"image_generation_tool"`
}

// DefaultImportOptions are the import settings when the config sets none.
var DefaultImportOptions = ImportOptions{CodexIdentity: true, ImageGenerationTool: true}

// Package IDs of the extensions today's provider sections and auth files map to.
const (
	Codex            = "pluxy/codex"
	Claude           = "pluxy/claude"
	Gemini           = "pluxy/gemini"
	Kimi             = "pluxy/kimi"
	XAI              = "pluxy/xai"
	Antigravity      = "pluxy/antigravity"
	Meta             = "pluxy/meta"
	Devin            = "pluxy/devin"
	OpenAICompatible = "pluxy/openai-compatible"
)

// channels are the oauth.model-alias and oauth.excluded-models channels today's core has, and
// the package whose accounts added by login they apply to (core/sdk/cliproxy/auth
// OAuthModelAliasChannel); a channel without one has no extension here.
var channels = map[string]string{
	"codex": Codex, "claude": Claude, "vertex": Gemini, "kimi": Kimi, "kimi-ai": Kimi, "kimi.ai": Kimi,
	"kimi.com": Kimi, "xai": XAI, "meta": Meta, "antigravity": Antigravity, "devin": Devin,
}

// newKeys are this core's own settings, which keep their JSON names in a config.yaml.
var newKeys = []string{"packages", "extensions", "extension", "extension_pages", "drop_policy", "token_count_estimate", "credentials_dir", "cors"}

// moves are today's older spellings and the v8 paths that replace them, from
// core/internal/config/config_v8.go. More specific paths come before the paths that contain
// them. When both are present the v8 value wins, field by field.
var moves = [][2]string{
	{"host", "server.host"}, {"port", "server.port"},
	{"max-request-body-bytes", "server.max-request-body-bytes"}, {"trusted-proxies", "server.trusted-proxies"},
	{"github-token", "server.github-token"}, {"tls", "server.tls"}, {"commercial-mode", "server.commercial-mode"},
	{"discovery", "server.discovery"}, {"response-compression", "server.response-compression"},
	{"remote-management", "management"},
	{"credential-concurrency", "credentials.concurrency"}, {"credential-in-flight", "credentials.in-flight"},
	{"force-model-prefix", "routing.force-model-prefix"},
	{"request-retry", "routing.retry.request-retry"}, {"max-retry-credentials", "routing.retry.max-retry-credentials"},
	{"max-retry-interval", "routing.retry.max-retry-interval"},
	{"disable-cooling", "routing.cooldown.disable-cooling"}, {"save-cooldown-status", "routing.cooldown.save-cooldown-status"},
	{"transient-error-cooldown-seconds", "routing.cooldown.transient-error-cooldown-seconds"},
	{"quota-cooldown-response-mode", "routing.cooldown.quota-response-mode"},
	{"proxy-url", "requests.proxy-url"}, {"tls-handshake-timeout-seconds", "requests.tls-handshake-timeout-seconds"},
	{"passthrough-headers", "requests.passthrough-headers"}, {"nonstream-keepalive-interval", "requests.nonstream-keepalive-interval"},
	{"streaming", "requests.streaming"}, {"payload", "requests.payload"},
	{"auth-dir", "oauth.auth-dir"}, {"auth-auto-refresh-workers", "oauth.auth-auto-refresh-workers"},
	{"oauth-model-alias", "oauth.model-alias"}, {"oauth-excluded-models", "oauth.excluded-models"},
	{"oauth-request-scoped-errors", "oauth.request-scoped-errors"}, {"oauth-settings", "oauth.settings"},
	{"ws-auth", "oauth.providers.aistudio.ws-auth"},
	{"oauth.providers.codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"providers.codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"codex.optimize-multi-agent-v2", "client.codex.optimize-multi-agent-v2"},
	{"oauth.providers.codex.disable-codex-cloaking", "upstream.codex.disable-codex-cloaking"},
	{"oauth.providers.codex.stream-bootstrap-buffering", "upstream.codex.stream-bootstrap-buffering"},
	{"oauth.providers.codex.stream-bootstrap-timeout", "upstream.codex.stream-bootstrap-timeout"},
	{"oauth.providers.codex.orphan-delegation-compatibility", "upstream.codex.orphan-delegation-compatibility"},
	{"oauth.providers.codex.model-level-cooling", "upstream.codex.model-level-cooling"},
	{"oauth.providers.codex.response-steering", "upstream.codex.response-steering"},
	{"oauth.providers.codex.upstream-request-compression", "upstream.codex.upstream-request-compression"},
	{"oauth.providers.codex.upstream-request-compression-min-bytes", "upstream.codex.upstream-request-compression-min-bytes"},
	{"oauth.providers.codex.websocket-compression", "upstream.codex.websocket-compression"},
	{"oauth.providers.claude.claude-code.disable-cloaking-model-list", "upstream.claude.disable-cloaking-model-list"},
	{"oauth.providers.claude", "upstream.claude"}, {"oauth.providers.xai", "upstream.xai"},
	{"codex.disable-codex-cloaking", "upstream.codex.disable-codex-cloaking"},
	{"codex.stream-bootstrap-buffering", "upstream.codex.stream-bootstrap-buffering"},
	{"codex.stream-bootstrap-timeout", "upstream.codex.stream-bootstrap-timeout"},
	{"codex.orphan-delegation-compatibility", "upstream.codex.orphan-delegation-compatibility"},
	{"codex.model-level-cooling", "upstream.codex.model-level-cooling"},
	{"codex.response-steering", "upstream.codex.response-steering"},
	{"codex.upstream-request-compression", "upstream.codex.upstream-request-compression"},
	{"codex.upstream-request-compression-min-bytes", "upstream.codex.upstream-request-compression-min-bytes"},
	{"codex.websocket-compression", "upstream.codex.websocket-compression"},
	{"codex-header-defaults", "oauth.providers.codex.header-defaults"}, {"codex", "oauth.providers.codex"},
	{"claude-header-defaults", "upstream.claude.header-defaults"},
	{"disable-claude-cloak-mode", "upstream.claude.disable-claude-cloak-mode"},
	{"claude-code", "upstream.claude"}, {"claude", "upstream.claude"},
	{"antigravity-signature-cache-enabled", "oauth.providers.antigravity.signature-cache-enabled"},
	{"antigravity-signature-bypass-strict", "oauth.providers.antigravity.signature-bypass-strict"},
	{"quota-exceeded.antigravity-credits", "oauth.providers.antigravity.antigravity-credits"},
	{"antigravity", "oauth.providers.antigravity"},
	{"xai", "upstream.xai"}, {"devin", "oauth.providers.devin"},
	{"disable-image-generation", "multimedia.disable-image-generation"},
	{"gpt-image-2-base-model", "multimedia.gpt-image-2-base-model"},
	{"video-result-auth-cache-ttl", "multimedia.video-result-auth-cache-ttl"},
	{"debug", "observability.logs.debug"}, {"logging-to-file", "observability.logs.logging-to-file"},
	{"logs-max-total-size-mb", "observability.logs.logs-max-total-size-mb"}, {"request-log", "observability.logs.request-log"},
	{"error-logs-max-files", "observability.logs.error-logs-max-files"},
	{"usage-statistics-enabled", "observability.usage.usage-statistics-enabled"},
	{"redis-usage-queue-retention-seconds", "observability.usage.redis-usage-queue-retention-seconds"},
	{"pprof", "observability.pprof"},
}

// family is one provider key section.
type family struct {
	v8, old, extension string
}

var families = []family{
	{"codex", "codex-api-key", Codex},
	{"claude", "claude-api-key", Claude},
	{"gemini", "gemini-api-key", Gemini},
	{"vertex", "vertex-api-key", Gemini},
	{"xai", "xai-api-key", XAI},
	{"openai-compatibility", "openai-compatibility", OpenAICompatible},
	{"meta", "meta-api-key", ""},
	{"interactions", "interactions-api-key", ""},
}

// Statuses of a setting, as next/README.md names them.
const (
	supported = iota
	later
	dropped
	dead
)

type rule struct {
	status int
	note   string // the slice that brings it (later) or why it is dropped
}

// rules classify each setting by its v8 path. A rule covers its path and everything below it.
// In provider sections, "*" stands for the section and a key's fields are looked up as the
// group's. Settings the translation reads are supported.
var rules = map[string]rule{
	"config-version": {supported, ""},
	"server.host":    {supported, ""}, "server.port": {supported, ""},
	"access.api-keys":                       {supported, ""},
	"management.secret-key":                 {supported, ""},
	"management.allow-remote":               {supported, ""},
	"management.disable-control-panel":      {dropped, "the management key turns the panel on and off"},
	"management.disable-auto-update-panel":  {dropped, "the panel is built into core"},
	"management.panel-github-repository":    {dropped, "the panel is built into core"},
	"server.github-token":                   {dropped, "the panel is built into core"},
	"management.base-url":                   {dropped, "no terminal UI"},
	"server.max-request-body-bytes":         {dropped, "requests are capped at 24 MiB"},
	"server.response-compression":           {later, ""},
	"server.trusted-proxies":                {supported, ""},
	"server.tls":                            {supported, ""},
	"server.commercial-mode":                {dropped, "it turns off features this core does not have"},
	"server.discovery":                      {later, ""},
	"server.discovery.advertise-management": {dead, ""},

	"routing.strategy":                                  {supported, ""},
	"routing.session-affinity":                          {supported, ""},
	"routing.session-affinity-ttl":                      {supported, ""},
	"routing.session-affinity-subagents":                {later, ""},
	"routing.session-affinity-rebind-on":                {later, ""},
	"routing.force-model-prefix":                        {supported, ""},
	"routing.retry.max-retry-credentials":               {supported, ""},
	"routing.retry.max-retry-interval":                  {supported, ""},
	"routing.retry.request-retry":                       {supported, ""},
	"routing.cooldown.disable-cooling":                  {supported, ""},
	"routing.cooldown.transient-error-cooldown-seconds": {supported, ""},
	"routing.cooldown.quota-response-mode":              {supported, ""},
	"routing.cooldown.save-cooldown-status":             {dropped, "no need found"},
	"quota-exceeded.switch-project":                     {dead, ""},
	"quota-exceeded.switch-preview-model":               {dead, ""},

	"requests.proxy-url":                     {later, ""},
	"requests.payload":                       {later, ""},
	"requests.tls-handshake-timeout-seconds": {dropped, "off by default and no need found"},
	"requests.passthrough-headers":           {dropped, "off by default and no need found"},
	"requests.nonstream-keepalive-interval":  {dropped, "off by default and no need found"},
	"requests.streaming":                     {dropped, "off by default and no need found"},

	"client.codex.enable-apply-patch":      {later, ""},
	"client.codex.optimize-multi-agent-v2": {later, ""},
	"client.codex.web-search-result-cache": {dropped, "off by default and no need found"},
	"client.claude.show-codex-commentary":  {dropped, "off by default and no need found"},

	"upstream.codex.disable-codex-cloaking":                 {supported, ""},
	"upstream.codex.websocket-compression":                  {supported, ""}, // reported in translate
	"upstream.codex.model-level-cooling":                    {supported, ""},
	"upstream.claude.model-level-cooling":                   {supported, ""},
	"upstream.codex.upstream-request-compression":           {later, ""},
	"upstream.codex.upstream-request-compression-min-bytes": {later, ""},
	"upstream.codex.stream-bootstrap-buffering":             {later, ""},
	"upstream.codex.stream-bootstrap-timeout":               {later, ""},
	"upstream.codex.response-steering":                      {dropped, "experimental, no need found"},
	"upstream.codex.orphan-delegation-compatibility":        {dropped, "off by default and no need found"},
	"oauth.providers.codex.header-defaults":                 {dropped, "no need found"},
	"upstream.claude":                                       {dropped, "making other clients look like Claude Code is out of scope"},
	"upstream.claude.tool-search":                           {later, ""},
	"upstream.claude.header-defaults":                       {dropped, "making other clients look like Claude Code is out of scope"},
	"upstream.xai.inject-x-search":                          {later, ""},

	"oauth.auth-dir":                                       {supported, ""},
	"oauth.auth-auto-refresh-workers":                      {dropped, "each account is renewed shortly before it expires"},
	"oauth.model-alias":                                    {supported, ""}, // reported in oauthModels
	"oauth.excluded-models":                                {supported, ""}, // reported in oauthModels
	"oauth.settings":                                       {dropped, "needs a model catalog, and no page uses it"},
	"oauth.request-scoped-errors":                          {later, ""},
	"oauth.providers.aistudio.ws-auth":                     {later, "the AI Studio relay is not built"},
	"oauth.providers.codex.live-media-relay":               {dropped, "realtime and WebRTC are out of scope"},
	"oauth.providers.codex.signin-with-chatgpt-identifier": {later, ""},
	"oauth.providers.antigravity":                          {later, ""},
	"oauth.providers.devin":                                {later, ""},

	"multimedia.disable-image-generation":                     {supported, ""},
	"multimedia.gpt-image-2-base-model":                       {later, ""},
	"multimedia.video-result-auth-cache-ttl":                  {later, ""},
	"observability.logs":                                      {later, ""},
	"observability.logs.logging-to-file":                      {supported, ""},
	"observability.logs.logs-max-total-size-mb":               {supported, ""},
	"observability.logs.debug":                                {dropped, "this core writes no debug-level log lines"},
	"observability.logs.request-log":                          {supported, ""},
	"observability.logs.error-logs-max-files":                 {supported, ""},
	"observability.usage.usage-statistics-enabled":            {supported, ""},
	"observability.usage.redis-usage-queue-retention-seconds": {dropped, "reading usage no longer removes it; the record keeps requests by usage.max_events and usage.max_age_ms"},
	"observability.pprof":                                     {dropped, "a developer tool, off by default"},
	"models":                                                  {later, ""},
	"plugins":                                                 {dropped, "replaced by packages"},
	"credentials":                                             {dropped, "only the Home control plane used them"},

	// Provider sections: group fields, and a key's fields (which override the group's).
	"api-keys.*[].name":                                        {supported, ""},
	"api-keys.*[].base-url":                                    {supported, ""},
	"api-keys.*[].priority":                                    {supported, ""},
	"api-keys.*[].api-key":                                     {supported, ""},
	"api-keys.*[].weight":                                      {supported, ""},
	"api-keys.*[].models[].name":                               {supported, ""},
	"api-keys.*[].models[].alias":                              {supported, ""},
	"api-keys.*[].models[].display-name":                       {later, ""},
	"api-keys.*[].models[].max-context-length":                 {later, ""},
	"api-keys.*[].models[].force-mapping":                      {later, ""},
	"api-keys.*[].models[].is-compat":                          {later, ""},
	"api-keys.*[].models[].thinking":                           {later, ""},
	"api-keys.*[].models[].image":                              {later, ""},
	"api-keys.*[].models[].input-modalities":                   {later, ""},
	"api-keys.*[].models[].output-modalities":                  {later, ""},
	"api-keys.*[].models[].use-max-completion-tokens":          {later, ""},
	"api-keys.*[].models[].support-configuration-update":       {later, ""},
	"api-keys.*[].models[].openapi":                            {later, ""},
	"api-keys.*[].prefix":                                      {supported, ""},
	"api-keys.*[].excluded-models":                             {supported, ""},
	"api-keys.*[].headers":                                     {later, ""},
	"api-keys.*[].proxy-url":                                   {later, ""},
	"api-keys.*[].connection-pool":                             {later, ""},
	"api-keys.*[].request-scoped-errors":                       {later, ""},
	"api-keys.*[].disable-cooling":                             {supported, ""},
	"api-keys.*[].request-retry":                               {supported, ""},
	"api-keys.codex[].websockets":                              {supported, ""},
	"api-keys.codex[].disable-codex-cloaking":                  {supported, ""},
	"api-keys.codex[].alpha-search":                            {supported, ""},
	"api-keys.xai[].websockets":                                {later, ""},
	"api-keys.vertex[].interactions":                           {later, ""},
	"api-keys.vertex[].openapi":                                {later, ""},
	"api-keys.vertex[].openapi-location":                       {later, ""},
	"api-keys.openai-compatibility[].disabled":                 {supported, ""},
	"api-keys.openai-compatibility[].support-prompt-cache-key": {later, ""},
	"api-keys.openai-compatibility[].excluded-models":          {dead, ""}, // today's core has no such key there
	"api-keys.claude[].cloak":                                  {dropped, "making other clients look like Claude Code is out of scope"},
	"api-keys.claude[].fingerprint-profile":                    {dropped, "making other clients look like Claude Code is out of scope"},
	"api-keys.claude[].native-tool-search":                     {later, ""},
	"api-keys.claude[].rebuild-mid-system-message":             {later, ""},
	"api-keys.claude[].experimental-cch-signing":               {dead, ""},
	"api-keys.meta":                                            {later, ""},
	"api-keys.interactions":                                    {later, ""},
}

// tree is a decoded YAML mapping.
type tree = map[string]any

// Translate turns the contents of a config.yaml written for today's core into this core's
// config. It fails only where today's core would refuse the file too (it is not a mapping, a
// key appears twice, config-version is not 8) or a value has the wrong type.
func Translate(b []byte) (*Result, error) {
	var root any
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	t, ok := root.(tree)
	if !ok {
		if root == nil {
			t = tree{}
		} else {
			return nil, errors.New("the config must be a mapping")
		}
	}
	x := &translator{t: t, out: map[string]any{}, res: &Result{Import: DefaultImportOptions}}
	if err := x.run(); err != nil {
		return nil, err
	}
	x.res.Config = x.out
	return x.res, nil
}

type translator struct {
	t   tree
	out map[string]any
	res *Result

	laterKeys, droppedKeys, unknownKeys []string
	notes                               []string // notices about one setting each
}

func (x *translator) run() error {
	if v, ok := get(x.t, "config-version"); ok {
		if n, isInt := v.(int); !isInt || n != 8 {
			return errors.New("unsupported config-version (expected 8)")
		}
	}
	for _, k := range newKeys {
		if v, ok := x.t[k]; ok {
			x.out[k] = v
			delete(x.t, k)
		}
	}
	// The older layout's client keys are a root api-keys list; the v8 root api-keys is a
	// mapping of provider sections.
	var providers tree
	switch v := x.t["api-keys"].(type) {
	case []any:
		if _, ok := get(x.t, "access.api-keys"); !ok {
			set(x.t, "access.api-keys", v)
		}
		delete(x.t, "api-keys")
	case tree:
		providers = v
		delete(x.t, "api-keys")
	case nil:
		delete(x.t, "api-keys")
	default:
		return errors.New("api-keys must be a list of client keys or a mapping of provider sections")
	}
	for _, m := range moves {
		if v, ok := get(x.t, m[0]); ok {
			if cur, has := get(x.t, m[1]); has {
				set(x.t, m[1], merge(cur, v))
			} else {
				set(x.t, m[1], v)
			}
			del(x.t, m[0])
		}
	}

	for _, p := range readPaths {
		if err := mappingsOn(x.t, p); err != nil {
			return err
		}
	}
	if err := x.server(); err != nil {
		return err
	}
	if err := x.routing(); err != nil {
		return err
	}
	if err := x.options(); err != nil {
		return err
	}
	if err := x.observability(); err != nil {
		return err
	}
	if err := x.providers(providers); err != nil {
		return err
	}
	if err := x.oauthModels(); err != nil {
		return err
	}
	x.classify("", x.t, nil)

	n := &x.res.Notices
	if len(x.laterKeys) > 0 {
		*n = append(*n, "config.yaml: not supported yet, ignored: "+strings.Join(dedupe(x.laterKeys), ", "))
	}
	if len(x.droppedKeys) > 0 {
		*n = append(*n, "config.yaml: dropped, ignored: "+strings.Join(dedupe(x.droppedKeys), ", "))
	}
	if len(x.unknownKeys) > 0 {
		*n = append(*n, "config.yaml: unknown keys, ignored: "+strings.Join(dedupe(x.unknownKeys), ", "))
	}
	*n = append(*n, x.notes...)
	return nil
}

// readPaths are the settings the translation reads outside the provider sections. Every
// mapping on the way to one must be a mapping (or empty), as today's core requires: a
// malformed parent must not make a setting such as the client keys read as absent.
var readPaths = []string{
	"server.host", "server.port", "access.api-keys", "management.secret-key", "management.allow-remote",
	"server.tls.enable", "server.tls.cert", "server.tls.key", "server.trusted-proxies",
	"routing.strategy", "routing.session-affinity", "routing.session-affinity-ttl",
	"routing.retry.max-retry-credentials", "routing.retry.max-retry-interval", "routing.retry.request-retry",
	"routing.force-model-prefix", "oauth.model-alias", "oauth.excluded-models",
	"upstream.codex.disable-codex-cloaking", "upstream.codex.websocket-compression",
	"multimedia.disable-image-generation", "oauth.auth-dir", "observability.usage.usage-statistics-enabled",
	"observability.logs.logging-to-file", "observability.logs.logs-max-total-size-mb",
	"observability.logs.request-log", "observability.logs.error-logs-max-files",
}

// mappingsOn checks that every value on the way to path, below the root, is a mapping or
// empty.
func mappingsOn(t tree, path string) error {
	parts := strings.Split(path, ".")
	var cur any = t
	for i, k := range parts[:len(parts)-1] {
		v, ok := cur.(tree)[k]
		if !ok || v == nil {
			return nil
		}
		if _, isMap := v.(tree); !isMap {
			return fmt.Errorf("%s must be a mapping", strings.Join(parts[:i+1], "."))
		}
		cur = v
	}
	return nil
}

// bcryptHash matches a bcrypt hash, which today's core may have written as its management key.
var bcryptHash = regexp.MustCompile(`^\$2[abxy]?\$\d{2}\$[./A-Za-z0-9]{53}$`)

func (x *translator) server() error {
	host, err := str(x.t, "server.host")
	if err != nil {
		return err
	}
	port, err := integer(x.t, "server.port")
	if err != nil {
		return err
	}
	if port == 0 {
		port = 8317 // today's core's default
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("server.port %d is not a port", port)
	}
	x.out["listen"] = net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))

	if on, err := flag(x.t, "server.tls.enable"); err != nil {
		return err
	} else if on {
		cert, err := str(x.t, "server.tls.cert")
		if err != nil {
			return err
		}
		key, err := str(x.t, "server.tls.key")
		if err != nil {
			return err
		}
		x.out["tls"] = map[string]any{"enable": true, "cert": strings.TrimSpace(cert), "key": strings.TrimSpace(key)}
	}

	if v, ok := get(x.t, "server.trusted-proxies"); ok && v != nil {
		proxies, err := stringList(v, "server.trusted-proxies")
		if err != nil {
			return err
		}
		if len(proxies) > 0 {
			x.out["trusted_proxies"] = proxies
		}
	}

	if v, ok := get(x.t, "access.api-keys"); ok && v != nil {
		keys, err := stringList(v, "access.api-keys")
		if err != nil {
			return err
		}
		x.out["client_keys"] = keys
	}

	secret, err := str(x.t, "management.secret-key")
	if err != nil {
		return err
	}
	// The key is used as written, as today's core uses it: a space in it is part of it.
	switch {
	case bcryptHash.MatchString(secret):
		x.notes = append(x.notes, "config.yaml: management.secret-key is a bcrypt hash, which is not supported yet: the management API stays off; set the key itself")
	case secret != "":
		x.out["management_key"] = secret
	}
	if remote, err := flag(x.t, "management.allow-remote"); err != nil {
		return err
	} else if remote {
		x.out["management_allow_remote"] = true
	}
	return nil
}

func (x *translator) routing() error {
	r := map[string]any{}
	s, err := str(x.t, "routing.strategy")
	if err != nil {
		return err
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "round-robin":
	case "weighted-round-robin", "weightedroundrobin", "wrr", "weighted":
		r["strategy"] = "weighted"
	case "fill-first", "fillfirst", "ff":
		r["strategy"] = "fill-first"
	case "least-active":
		r["strategy"] = "least-active"
	default:
		// Today's core uses round-robin for a name it does not know.
		x.notes = append(x.notes, fmt.Sprintf("config.yaml: routing.strategy %q is not known: using round-robin", s))
	}
	if on, err := flag(x.t, "routing.session-affinity"); err != nil {
		return err
	} else if on {
		ttl := time.Hour
		if s, err := str(x.t, "routing.session-affinity-ttl"); err != nil {
			return err
		} else if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
			ttl = max(d, time.Second) // as today's core reads it
		}
		r["session_affinity"] = map[string]any{"ttl_ms": ttl.Milliseconds()}
	}
	if on, err := flag(x.t, "routing.force-model-prefix"); err != nil {
		return err
	} else if on {
		r["force_model_prefix"] = true
	}
	if n, err := integer(x.t, "routing.retry.max-retry-credentials"); err != nil {
		return err
	} else if n > 0 {
		r["max_accounts"] = n
	}
	// Rounds as today's core runs them; both settings default to 0 there (no extra rounds, no
	// waits), whatever its example config shows.
	retry, err := integer(x.t, "routing.retry.request-retry")
	if err != nil {
		return err
	}
	r["request_retry"] = max(retry, 0)
	if wait, err := integer(x.t, "routing.retry.max-retry-interval"); err != nil {
		return err
	} else if wait > 0 {
		r["max_wait_ms"] = int64(wait) * 1000
	}
	if on, err := flag(x.t, "routing.cooldown.disable-cooling"); err != nil {
		return err
	} else if on {
		r["disable_cooling"] = true
	}
	// 0 is today's fixed minute; this core's default backs off from a second to a minute.
	if n, err := integer(x.t, "routing.cooldown.transient-error-cooldown-seconds"); err != nil {
		return err
	} else if n > 0 {
		r["transient_error_cooldown_ms"] = int64(n) * 1000
	} else if n < 0 {
		r["transient_error_cooldown_ms"] = -1
	}
	// Read as today's core reads it: letter case and spaces aside; anything else is an error.
	if s, err := str(x.t, "routing.cooldown.quota-response-mode"); err != nil {
		return err
	} else if mode := strings.ToLower(strings.TrimSpace(s)); mode == "usage-limit" {
		r["quota_response_mode"] = mode
	} else if mode != "" && mode != "retryable" {
		return fmt.Errorf("invalid routing.cooldown.quota-response-mode %q: want \"retryable\" or \"usage-limit\"", s)
	}
	if len(r) > 0 {
		x.out["routing"] = r
	}
	return nil
}

// options reads the settings that change how accounts are set up.
func (x *translator) options() error {
	if off, err := flag(x.t, "upstream.codex.disable-codex-cloaking"); err != nil {
		return err
	} else if off {
		x.res.Import.CodexIdentity = false
	}
	switch v, _ := get(x.t, "multimedia.disable-image-generation"); v {
	case nil, false, "false", "":
	case true, "chat", "passthrough", "true":
		x.res.Import.ImageGenerationTool = false
	default:
		return fmt.Errorf("multimedia.disable-image-generation must be false, true, \"chat\" or \"passthrough\"")
	}
	if v, ok := get(x.t, "upstream.codex.websocket-compression"); ok && v != nil {
		if on, err := flag(x.t, "upstream.codex.websocket-compression"); err != nil {
			return err
		} else if !on {
			x.notes = append(x.notes, "config.yaml: upstream.codex.websocket-compression: false is not supported: compression stays on")
		}
	}
	dir, err := str(x.t, "oauth.auth-dir")
	if err != nil {
		return err
	}
	x.res.AuthDir = strings.TrimSpace(dir)
	return nil
}

// observability reads the log and usage settings.
func (x *translator) observability() error {
	logs := tree{}
	if on, err := flag(x.t, "observability.logs.logging-to-file"); err != nil {
		return err
	} else if on {
		logs["to_file"] = true
	}
	// As in today's core, a negative cap is none.
	if n, err := integer(x.t, "observability.logs.logs-max-total-size-mb"); err != nil {
		return err
	} else if n > 0 {
		logs["max_total_size_mb"] = n
	}
	if on, err := flag(x.t, "observability.logs.request-log"); err != nil {
		return err
	} else if on {
		logs["request_log"] = true
	}
	// As in today's core, a negative count is the default, 10.
	if v, ok := get(x.t, "observability.logs.error-logs-max-files"); ok && v != nil {
		n, err := integer(x.t, "observability.logs.error-logs-max-files")
		if err != nil {
			return err
		}
		if n >= 0 && n != 10 {
			logs["error_logs_max_files"] = n
		}
	}
	if len(logs) > 0 {
		x.out["logs"] = logs
	}
	if v, ok := get(x.t, "observability.usage.usage-statistics-enabled"); ok && v != nil {
		on, err := boolean_(v, "observability.usage.usage-statistics-enabled")
		if err != nil {
			return err
		}
		x.out["usage"] = tree{"enabled": on}
	}
	return nil
}

// providers turns the provider key sections, in either layout, into accounts of the packages
// that serve them. v8 is the v8 api-keys mapping, or nil.
func (x *translator) providers(v8 tree) error {
	var accounts = map[string][]any{}
	used := map[string]bool{}
	for _, f := range families {
		var groups []any
		if v, ok := v8[f.v8]; ok && v != nil {
			list, isList := v.([]any)
			if !isList {
				return fmt.Errorf("api-keys.%s must be a list", f.v8)
			}
			groups = list
		} else if v, ok := x.t[f.old]; ok && v != nil {
			list, isList := v.([]any)
			if !isList {
				return fmt.Errorf("%s must be a list", f.old)
			}
			for _, e := range list {
				g, isMap := e.(tree)
				if !isMap {
					return fmt.Errorf("%s entries must be mappings", f.old)
				}
				groups = append(groups, oldGroup(g, f.v8 == "openai-compatibility"))
			}
		}
		delete(v8, f.v8)
		delete(x.t, f.old)
		if len(groups) == 0 {
			continue
		}
		path := "api-keys." + f.v8
		if f.extension == "" {
			x.classify(path, groups, nil)
			continue
		}
		n := 0
		for gi, gv := range groups {
			g, ok := gv.(tree)
			if !ok {
				return fmt.Errorf("%s[%d] must be a mapping", path, gi)
			}
			keys, _ := g["keys"].([]any)
			if g["keys"] != nil && keys == nil {
				return fmt.Errorf("%s[%d].keys must be a list", path, gi)
			}
			name, err := scalar(g["name"], path+".name")
			if err != nil {
				return err
			}
			disabled, err := boolean_(g["disabled"], path+".disabled")
			if err != nil {
				return err
			}
			if len(keys) == 0 && f.v8 == "openai-compatibility" && !disabled {
				x.keyless(path)
			}
			for ki, kv := range keys {
				k, ok := kv.(tree)
				if !ok {
					return fmt.Errorf("%s[%d].keys[%d] must be a mapping", path, gi, ki)
				}
				n++
				if disabled {
					continue
				}
				if key, err := scalar(field(g, k, "api-key"), path+".keys.api-key"); err != nil {
					return err
				} else if strings.TrimSpace(key) == "" {
					x.keyless(path)
					continue
				}
				id := f.old + "-" + strconv.Itoa(n)
				if name != "" {
					id = name
					if len(keys) > 1 {
						id = name + "-" + strconv.Itoa(ki+1)
					}
				}
				a, err := x.account(f, g, k, accountID(id, used), path)
				if err != nil {
					return err
				}
				accounts[f.extension] = append(accounts[f.extension], a)
			}
			// The key list is read; classify the rest of the group and each key's own fields.
			rest := tree{}
			for k, v := range g {
				if k != "keys" {
					rest[k] = v
				}
			}
			x.classify(path+"[]", rest, nil)
			for _, kv := range keys {
				x.classify(path+"[]", kv, nil)
			}
		}
	}
	for k := range v8 {
		x.unknownKeys = append(x.unknownKeys, "api-keys."+k)
	}
	if len(accounts) == 0 {
		return nil
	}
	exts, _ := x.out["extensions"].(tree)
	if x.out["extensions"] != nil && exts == nil {
		return errors.New("extensions must be a mapping")
	}
	if exts == nil {
		exts = tree{}
	}
	for id, list := range accounts {
		e, _ := exts[id].(tree)
		if exts[id] != nil && e == nil {
			return fmt.Errorf("extensions[%q] must be a mapping", id)
		}
		if e == nil {
			e = tree{}
		}
		have, _ := e["accounts"].([]any)
		e["accounts"] = append(have, list...)
		exts[id] = e
	}
	x.out["extensions"] = exts
	return nil
}

// oldGroup turns one entry of an older-layout section into a v8 group: an openai-compatibility
// entry is already a group, with api-key-entries as its keys; any other entry is a group with
// one key.
func oldGroup(e tree, compat bool) tree {
	g := tree{}
	for k, v := range e {
		g[k] = v
	}
	if compat {
		g["keys"] = g["api-key-entries"]
		delete(g, "api-key-entries")
		return g
	}
	key := tree{}
	for _, k := range []string{"api-key", "weight", "websockets", "disable-codex-cloaking", "alpha-search", "interactions", "openapi", "openapi-location"} {
		if v, ok := g[k]; ok {
			key[k] = v
			delete(g, k)
		}
	}
	g["keys"] = []any{key}
	return g
}

// field returns a key's value for name, else its group's: a key's fields override the group's
// (a null key field inherits, as in today's core).
func field(g, k tree, name string) any {
	if v, ok := k[name]; ok && v != nil {
		return v
	}
	return g[name]
}

func (x *translator) account(f family, g, k tree, id, path string) (map[string]any, error) {
	key, _ := scalar(field(g, k, "api-key"), path)
	a := map[string]any{"id": id, "credential": map[string]any{"kind": "api_key", "api_key": strings.TrimSpace(key)}}
	if p := field(g, k, "priority"); p != nil {
		n, ok := p.(int)
		if !ok {
			return nil, fmt.Errorf("%s.priority must be a whole number", path)
		}
		a["priority"] = n
	}
	if d := field(g, k, "disable-cooling"); d != nil {
		on, ok := d.(bool)
		if !ok {
			return nil, fmt.Errorf("%s.disable-cooling must be true or false", path)
		}
		a["disable_cooling"] = on
	}
	// An openai-compatibility group's count is its keys', which have none of their own there.
	rr := field(g, k, "request-retry")
	if f.v8 == "openai-compatibility" {
		rr = g["request-retry"]
	}
	if rr != nil {
		n, ok := rr.(int)
		if !ok {
			return nil, fmt.Errorf("%s.request-retry must be a whole number", path)
		}
		if n >= 0 { // negative inherits routing's count, as in today's core
			a["request_retry"] = n
		}
	}
	if w := k["weight"]; w != nil {
		n, ok := w.(int)
		if !ok || n < 0 || n > 1_000_000 {
			return nil, fmt.Errorf("%s: weight must be a whole number from 0 to 1000000", path)
		}
		a["weight"] = n
	}
	if mv := field(g, k, "models"); mv != nil {
		list, ok := mv.([]any)
		if !ok {
			return nil, fmt.Errorf("%s.models must be a list", path)
		}
		var models []any
		public := map[string]bool{}
		for _, m := range list {
			e, ok := m.(tree)
			if !ok {
				return nil, fmt.Errorf("%s.models entries must be mappings", path)
			}
			name, err := scalar(e["name"], path+".models.name")
			if err != nil {
				return nil, err
			}
			alias, err := scalar(e["alias"], path+".models.alias")
			if err != nil {
				return nil, err
			}
			// The name clients use is the alias, else the name; the first entry for a name
			// wins, as in today's core.
			name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
			if name == "" || public[cmp.Or(alias, name)] {
				continue
			}
			public[cmp.Or(alias, name)] = true
			if alias == "" || alias == name {
				models = append(models, name)
			} else {
				models = append(models, map[string]any{"name": name, "alias": alias})
			}
		}
		if len(models) > 0 {
			a["models"] = models
		}
	}
	if p, err := scalar(field(g, k, "prefix"), path+".prefix"); err != nil {
		return nil, err
	} else if p = modelPrefix(p); p != "" {
		a["prefix"] = p
	}
	if f.v8 != "openai-compatibility" {
		if v := field(g, k, "excluded-models"); v != nil {
			list, err := stringList(v, path+".excluded-models")
			if err != nil {
				return nil, err
			}
			if pats := patterns(list); len(pats) > 0 {
				a["excluded_models"] = pats
			}
		}
	}
	settings := map[string]any{}
	u, err := scalar(field(g, k, "base-url"), path+".base-url")
	if err != nil {
		return nil, err
	}
	if u = strings.TrimSpace(u); u != "" {
		settings["base_url"] = u
	}
	switch f.v8 {
	case "codex":
		ws, err := boolean_(field(g, k, "websockets"), path+".websockets")
		if err != nil {
			return nil, err
		}
		if ws {
			settings["websockets"] = true
		}
		search, err := boolean_(field(g, k, "alpha-search"), path+".alpha-search")
		if err != nil {
			return nil, err
		}
		if search {
			settings["alpha_search"] = true
		}
		off, err := boolean_(field(g, k, "disable-codex-cloaking"), path+".disable-codex-cloaking")
		if err != nil {
			return nil, err
		}
		if off {
			settings["codex_identity"] = false
		}
		if !x.res.Import.ImageGenerationTool {
			settings["image_generation_tool"] = false
		}
	case "vertex":
		settings["vertex"] = map[string]any{}
	}
	if len(settings) > 0 {
		a["settings"] = settings
	}
	return a, nil
}

// modelPrefix reads a prefix as today's core does: without surrounding spaces and slashes, and
// none at all when a slash is left inside.
func modelPrefix(p string) string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if strings.Contains(p, "/") {
		return ""
	}
	return p
}

// patterns reads excluded-models patterns as today's core does: trimmed, lower case, each once.
func patterns(list []string) []string {
	var out []string
	for _, p := range list {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// oauthModels reads oauth.model-alias and oauth.excluded-models, each a mapping of today's
// channels to rules, into the model_alias and excluded_models of the package each channel's
// accounts belong to (channels). They apply to the package's accounts added by a sign-in (not with an API key), as today's
// apply to accounts from its auth folder.
func (x *translator) oauthModels() error {
	exts, _ := x.out["extensions"].(tree)
	if x.out["extensions"] != nil && exts == nil {
		return errors.New("extensions must be a mapping")
	}
	if exts == nil {
		exts = tree{}
	}
	section := func(pkg string) (tree, error) {
		e, _ := exts[pkg].(tree)
		if exts[pkg] != nil && e == nil {
			return nil, fmt.Errorf("extensions[%q] must be a mapping", pkg)
		}
		if e == nil {
			e = tree{}
			exts[pkg] = e
		}
		return e, nil
	}
	read := func(path string) (tree, []string, error) {
		v, _ := get(x.t, path)
		del(x.t, path)
		if v == nil {
			return nil, nil, nil
		}
		m, ok := v.(tree)
		if !ok {
			return nil, nil, fmt.Errorf("%s must be a mapping of channels", path)
		}
		var names []string
		for ch := range m {
			names = append(names, ch)
		}
		slices.Sort(names)
		return m, names, nil
	}

	aliases, names, err := read("oauth.model-alias")
	if err != nil {
		return err
	}
	for _, ch := range names {
		list, ok := aliases[ch].([]any)
		if aliases[ch] != nil && !ok {
			return fmt.Errorf("oauth.model-alias.%s must be a list", ch)
		}
		var rules []any
		for _, v := range list {
			e, ok := v.(tree)
			if !ok {
				return fmt.Errorf("oauth.model-alias.%s entries must be mappings", ch)
			}
			name, err := scalar(e["name"], "oauth.model-alias."+ch+".name")
			if err != nil {
				return err
			}
			alias, err := scalar(e["alias"], "oauth.model-alias."+ch+".alias")
			if err != nil {
				return err
			}
			display, err := scalar(e["display-name"], "oauth.model-alias."+ch+".display-name")
			if err != nil {
				return err
			}
			fork, err := boolean_(e["fork"], "oauth.model-alias."+ch+".fork")
			if err != nil {
				return err
			}
			for k, v := range e {
				switch k {
				case "name", "alias", "display-name", "fork":
				case "force-mapping", "openapi":
					if !isZero(v) {
						x.laterKeys = append(x.laterKeys, "oauth.model-alias."+k)
					}
				default:
					x.unknownKeys = append(x.unknownKeys, "oauth.model-alias."+k)
				}
			}
			// As today's core reads them: an empty name or alias, or an alias that is the
			// name, does nothing; an alias counts once per channel, whatever its case.
			name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
			if name == "" || alias == "" || strings.EqualFold(name, alias) ||
				slices.ContainsFunc(rules, func(r any) bool { return strings.EqualFold(r.(map[string]any)["alias"].(string), alias) }) {
				continue
			}
			r := map[string]any{"name": name, "alias": alias}
			if fork {
				r["fork"] = true
			}
			if d := strings.TrimSpace(display); d != "" {
				r["display_name"] = d
			}
			rules = append(rules, r)
		}
		if len(rules) == 0 {
			continue
		}
		pkg, ok := channels[strings.ToLower(strings.TrimSpace(ch))]
		if !ok {
			x.notes = append(x.notes, fmt.Sprintf("config.yaml: oauth.model-alias.%s: no extension serves the %s accounts of today's core; ignored", ch, ch))
			continue
		}
		e, err := section(pkg)
		if err != nil {
			return err
		}
		have, _ := e["model_alias"].([]any)
		e["model_alias"] = append(have, rules...)
	}

	excluded, names, err := read("oauth.excluded-models")
	if err != nil {
		return err
	}
	for _, ch := range names {
		if excluded[ch] == nil {
			continue
		}
		list, err := stringList(excluded[ch], "oauth.excluded-models."+ch)
		if err != nil {
			return err
		}
		pats := patterns(list)
		if len(pats) == 0 {
			continue
		}
		pkg, ok := channels[strings.ToLower(strings.TrimSpace(ch))]
		if !ok {
			x.notes = append(x.notes, fmt.Sprintf("config.yaml: oauth.excluded-models.%s: no extension serves the %s accounts of today's core; ignored", ch, ch))
			continue
		}
		e, err := section(pkg)
		if err != nil {
			return err
		}
		have, _ := e["excluded_models"].([]any)
		for _, p := range pats {
			if !slices.Contains(have, any(p)) {
				have = append(have, p)
			}
		}
		e["excluded_models"] = have
	}
	if len(exts) > 0 {
		x.out["extensions"] = exts
	}
	return nil
}

var notIDChar = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// accountID makes id a valid account ID that is not in used yet, and records it.
func accountID(id string, used map[string]bool) string {
	id = AccountID(id)
	base := id
	for i := 2; used[strings.ToLower(id)]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	used[strings.ToLower(id)] = true
	return id
}

// AccountID turns s into a valid account ID: characters an ID cannot hold become "-", and it
// starts with a letter or digit, is at most 128 characters long and does not end in ".".
func AccountID(s string) string {
	s = notIDChar.ReplaceAllString(s, "-")
	s = strings.TrimLeft(s, "._-")
	if len(s) > 128 {
		s = s[:128]
	}
	s = strings.TrimRight(s, ".")
	if s == "" {
		s = "account"
	}
	return s
}

// defaults are today's core's defaults for settings this core does not support: a file that
// leaves one at its default asks for nothing, so it is not named.
var defaults = map[string]any{
	"server.response-compression.enabled":                     true,
	"server.response-compression.min-bytes":                   1024,
	"management.panel-github-repository":                      "https://github.com/router-for-me/Cli-Proxy-API-Management-Center",
	"routing.session-affinity-subagents":                      true,
	"routing.session-affinity-rebind-on":                      "any",
	"upstream.codex.upstream-request-compression-min-bytes":   1024,
	"upstream.claude.session-date-pin-days":                   7,
	"oauth.providers.aistudio.ws-auth":                        true,
	"oauth.providers.codex.signin-with-chatgpt-identifier":    "CLIProxyAPI",
	"oauth.providers.antigravity.antigravity-credits":         true,
	"oauth.providers.antigravity.signature-cache-enabled":     true,
	"multimedia.gpt-image-2-base-model":                       "gpt-5.4-mini",
	"multimedia.video-result-auth-cache-ttl":                  "3h",
	"observability.logs.error-logs-max-files":                 10,
	"observability.usage.redis-usage-queue-retention-seconds": 60,
}

// classify names, among the settings at path and below, those that are not supported, are
// dropped or are not known. inherited is the rule of the nearest mapping above that has one.
func (x *translator) classify(path string, v any, inherited *rule) {
	r, ok := lookup(path)
	if !ok && inherited != nil {
		r, ok = *inherited, true
	}
	if ok {
		if m, isMap := v.(tree); isMap && rulesBelow(path) && !switchedOff(m) {
			for k, c := range m {
				x.classify(path+"."+k, c, &r)
			}
			return
		}
		if asksNothing(path, v) {
			return
		}
		name := display(path)
		switch r.status {
		case later:
			if r.note != "" {
				name += " (" + r.note + ")"
			}
			x.laterKeys = append(x.laterKeys, name)
		case dropped:
			x.droppedKeys = append(x.droppedKeys, name+" ("+r.note+")")
		}
		return
	}
	switch v := v.(type) {
	case tree:
		if switchedOff(v) {
			return
		}
		for k, c := range v {
			p := k
			if path != "" {
				p = path + "." + k
			}
			x.classify(p, c, nil)
		}
	case []any:
		for _, c := range v {
			if _, isMap := c.(tree); isMap {
				p := path
				if !strings.HasSuffix(p, "[]") {
					p += "[]"
				}
				x.classify(p, c, nil)
			} else if !isZero(c) {
				x.unknownKeys = append(x.unknownKeys, display(path))
			}
		}
	default:
		if !asksNothing(path, v) {
			x.unknownKeys = append(x.unknownKeys, display(path))
		}
	}
}

// rulesBelow reports whether a setting below path has a rule of its own.
func rulesBelow(path string) bool {
	for k := range rules {
		if strings.HasPrefix(k, path+".") {
			return true
		}
	}
	return false
}

// switchedOff reports whether m is a feature's settings with its switch off (enabled or
// enable false or missing): the rest of its settings then ask for nothing.
func switchedOff(m tree) bool {
	for _, k := range []string{"enabled", "enable"} {
		if v, ok := m[k]; ok {
			return v != true
		}
	}
	return false
}

// asksNothing reports whether the value at path asks for nothing: it is off, zero, empty, at
// today's default, or a feature switched off.
func asksNothing(path string, v any) bool {
	switch v := v.(type) {
	case tree:
		if switchedOff(v) {
			return true
		}
		for k, c := range v {
			if !asksNothing(path+"."+k, c) {
				return false
			}
		}
		return true
	case []any:
		return len(v) == 0
	}
	if d, ok := defaults[path]; ok && d == v {
		return true
	}
	return isZero(v)
}

// lookup finds the rule for path: a provider path is looked up with its section as "*" when
// the section has no rule of its own.
func lookup(path string) (rule, bool) {
	if r, ok := rules[path]; ok {
		return r, true
	}
	if rest, ok := strings.CutPrefix(path, "api-keys."); ok {
		section, tail, _ := strings.Cut(rest, ".")
		if strings.HasSuffix(section, "[]") && tail != "" {
			if r, ok := rules["api-keys.*[]."+tail]; ok {
				return r, true
			}
		}
	}
	return rule{}, false
}

// display is how a setting is named in a notice.
func display(path string) string { return strings.ReplaceAll(path, "[]", "") }

func isZero(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case int:
		return v == 0
	case float64:
		return v == 0
	case string:
		s := strings.TrimSpace(v)
		return s == "" || s == "0"
	case []any:
		return len(v) == 0
	case tree:
		for _, c := range v {
			if !isZero(c) {
				return false
			}
		}
		return true
	}
	return false
}

func dedupe(s []string) []string {
	slices.Sort(s)
	return slices.Compact(s)
}

// get returns the value at a dotted path.
func get(t tree, path string) (any, bool) {
	var cur any = t
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(tree)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// set sets the value at a dotted path, making the mappings on the way.
func set(t tree, path string, v any) {
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		m, ok := t[k].(tree)
		if !ok {
			m = tree{}
			t[k] = m
		}
		t = m
	}
	t[parts[len(parts)-1]] = v
}

// del removes the value at a dotted path, and the mappings it leaves empty.
func del(t tree, path string) {
	parts := strings.Split(path, ".")
	if len(parts) == 1 {
		delete(t, path)
		return
	}
	m, ok := t[parts[0]].(tree)
	if !ok {
		return
	}
	del(m, strings.Join(parts[1:], "."))
	if len(m) == 0 {
		delete(t, parts[0])
	}
}

// merge returns cur with the fields of old it lacks, mapping by mapping: the v8 value wins.
func merge(cur, old any) any {
	c, ok1 := cur.(tree)
	o, ok2 := old.(tree)
	if !ok1 || !ok2 {
		return cur
	}
	for k, v := range o {
		if have, ok := c[k]; ok {
			c[k] = merge(have, v)
		} else {
			c[k] = v
		}
	}
	return c
}

func str(t tree, path string) (string, error) {
	v, ok := get(t, path)
	if !ok || v == nil {
		return "", nil
	}
	switch v := v.(type) {
	case string:
		return v, nil
	case int:
		return strconv.Itoa(v), nil
	}
	return "", fmt.Errorf("%s must be text", path)
}

func integer(t tree, path string) (int, error) {
	v, ok := get(t, path)
	if !ok || v == nil {
		return 0, nil
	}
	switch v := v.(type) {
	case int:
		return v, nil
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s must be a whole number", path)
}

func stringList(v any, path string) ([]string, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", path)
	}
	out := []string{}
	for _, e := range list {
		s, err := scalar(e, path)
		if err != nil {
			return nil, err
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// scalar reads a text setting as today's core's typed decoder does: a number written without
// quotes is its digits.
//
// shortcut: the digits are Go's rendering of the decoded number, so an unquoted number YAML
// reads differently from its text (a leading zero, an exponent) is not kept as written;
// decode yaml.Node scalars when such keys turn up.
func scalar(v any, path string) (string, error) {
	switch v := v.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case int:
		return strconv.Itoa(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("%s must be text", path)
}

// boolean_ reads a true or false setting; absent is false.
func boolean_(v any, path string) (bool, error) {
	switch v := v.(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	}
	return false, fmt.Errorf("%s must be true or false", path)
}

// flag reads the true or false setting at path.
func flag(t tree, path string) (bool, error) {
	v, _ := get(t, path)
	return boolean_(v, path)
}

// keyless reports a key with no api-key in the provider section at path.
func (x *translator) keyless(path string) {
	x.laterKeys = append(x.laterKeys, display(path)+" keys without an api-key (an upstream that needs no key)")
}
