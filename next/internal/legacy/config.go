// Package legacy reads what today's core (core/, the CLIProxyAPI fork) leaves behind: its
// config.yaml, with its key names in either layout it reads (v8 and the older spellings), and
// the credential files in its auth folder. It turns them into this core's config and
// credential records. The mapping is documented in next/README.md, "config.yaml with today's
// key names".
package legacy

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/internal/proxyurl"
	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/internal/upheader"
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
	// HashedManagementKey: management.secret-key holds a bcrypt hash of the key, which today's
	// core writes over the key and this core cannot use. The config then has no management_key.
	HashedManagementKey bool
	// Ledger accounts for every key of the file in an import report.
	Ledger *Ledger
	// OldListen is the address today's core listened on: every interface (0.0.0.0) without a host.
	OldListen string
	// ListenKeys are the report items of the keys that make up listen (server.host and
	// server.port, in whichever spelling the file uses), and of server.host when the file sets no
	// host: the import then has a row for it too.
	ListenKeys []string
	// Accounts maps each account the config's provider sections become to the item of the report
	// that names it (ConfigItem).
	Accounts map[string]string
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
	// Images is false when multimedia.disable-image-generation is true, which also turns off the
	// Images API routes: imported Codex accounts then get "images": false.
	Images bool `json:"images"`
	// InjectXSearch is true when upstream.xai.inject-x-search is set (or its older spelling): the
	// xAI accounts of the config and imported xAI logins then get "inject_x_search": true, which
	// injects xAI's native x_search tool. False by default.
	InjectXSearch bool `json:"inject_x_search"`
	// SIWCIdentifier is codex.signin-with-chatgpt-identifier, trimmed: imported Sign in with
	// ChatGPT accounts then get it as "signin_with_chatgpt_identifier". Empty: the extension's
	// default.
	SIWCIdentifier string `json:"signin_with_chatgpt_identifier,omitempty"`
	// ClaudeModelLevelCooling is upstream.claude.model-level-cooling: imported Claude accounts
	// then get "model_level_cooling": true.
	ClaudeModelLevelCooling bool `json:"model_level_cooling,omitempty"`
}

// DefaultImportOptions are the import settings when the config sets none.
var DefaultImportOptions = ImportOptions{CodexIdentity: true, ImageGenerationTool: true, Images: true}

// Package IDs of the extensions today's provider sections and auth files map to.
const (
	Antigravity      = "pluxy/antigravity" // archived: not shipped, built by hand (next/archive/antigravity)
	Codex            = "pluxy/codex"
	Claude           = "pluxy/claude"
	Gemini           = "pluxy/gemini"
	Kimi             = "pluxy/kimi"
	XAI              = "pluxy/xai"
	Meta             = "pluxy/meta"
	Devin            = "pluxy/devin"
	OpenAICompatible = "pluxy/openai-compatible"
)

// channels are the oauth.model-alias and oauth.excluded-models channels today's core has, and
// the package whose accounts added by login they apply to (core/sdk/cliproxy/auth
// OAuthModelAliasChannel); a channel without one has no extension here.
var channels = map[string]string{
	"codex": Codex, "claude": Claude, "antigravity": Antigravity, "vertex": Gemini, "kimi": Kimi, "kimi-ai": Kimi, "kimi.ai": Kimi,
	"kimi.com": Kimi, "xai": XAI, "meta": Meta, "devin": Devin,
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
	{"interactions", "interactions-api-key", Gemini},
}

// SectionLists returns the keys at the top of a config.yaml whose value is a list of provider key
// entries, in today's core's older spelling (codex-api-key and the rest). In the v8 layout the
// sections are the values of the top-level api-keys mapping.
func SectionLists() []string {
	var out []string
	for _, f := range families {
		out = append(out, f.old)
	}
	return out
}

// SectionNames returns the keys of the v8 api-keys mapping of a config.yaml that are provider key
// sections (codex, openai-compatibility and the rest).
func SectionNames() []string {
	var out []string
	for _, f := range families {
		out = append(out, f.v8)
	}
	return out
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
	note   string // what brings it (later) or why it is dropped
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
	"server.response-compression":           {dropped, "a reverse proxy compresses replies"},
	"server.trusted-proxies":                {supported, ""},
	"server.tls":                            {supported, ""},
	"server.commercial-mode":                {dropped, "it turns off features this core does not have"},
	"server.discovery":                      {dropped, "no client here reads the advertisement"},
	"server.discovery.advertise-management": {dead, ""},

	"routing.strategy":                                  {supported, ""},
	"routing.session-affinity":                          {supported, ""},
	"routing.session-affinity-ttl":                      {supported, ""},
	"routing.session-affinity-subagents":                {dropped, "a subagent stays on its session's account, and no need was found to spread subagents over the pool"},
	"routing.session-affinity-rebind-on":                {dropped, "no need found"},
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

	"requests.proxy-url":                     {supported, ""},
	"requests.payload":                       {supported, ""},
	"requests.tls-handshake-timeout-seconds": {dropped, "off by default and no need found"},
	"requests.passthrough-headers":           {dropped, "off by default and no need found"},
	"requests.nonstream-keepalive-interval":  {dropped, "off by default and no need found"},
	"requests.streaming":                     {dropped, "off by default and no need found"},

	"client.codex.enable-apply-patch":      {later, "built when someone uses a non-Codex model through the Codex client and needs apply_patch on it"},
	"client.codex.optimize-multi-agent-v2": {later, "built when Codex multi-agent v2 sub-agents need to reach non-Codex providers"},
	"client.codex.web-search-result-cache": {dropped, "off by default and no need found"},
	"client.claude.show-codex-commentary":  {dropped, "off by default and no need found"},

	"upstream.codex.disable-codex-cloaking":                 {supported, ""},
	"upstream.codex.websocket-compression":                  {supported, ""}, // reported in translate
	"upstream.codex.model-level-cooling":                    {supported, ""},
	"upstream.claude.model-level-cooling":                   {supported, ""},
	"upstream.codex.upstream-request-compression":           {dropped, "it needs a compression library this core does not carry, for an off-by-default saving no one has measured"},
	"upstream.codex.upstream-request-compression-min-bytes": {dropped, "it needs a compression library this core does not carry, for an off-by-default saving no one has measured"},
	"upstream.codex.stream-bootstrap-buffering":             {later, "built when pooled Codex accounts are seen failing on overload errors that moving the turn to another account would hide"},
	"upstream.codex.stream-bootstrap-timeout":               {later, "built when pooled Codex accounts are seen failing on overload errors that moving the turn to another account would hide"},
	"upstream.codex.response-steering":                      {dropped, "experimental, no need found"},
	"upstream.codex.orphan-delegation-compatibility":        {dropped, "off by default and no need found"},
	"oauth.providers.codex.header-defaults":                 {dropped, "no need found"},
	"upstream.claude":                                       {dropped, "making other clients look like Claude Code is out of scope"},
	"upstream.claude.tool-search":                           {dropped, "Codex's tool search on Claude accounts: off by default, with no panel or app control"},
	"upstream.claude.header-defaults":                       {dropped, "making other clients look like Claude Code is out of scope"},
	"upstream.xai.inject-x-search":                          {supported, ""},

	"oauth.auth-dir":                                       {supported, ""},
	"oauth.auth-auto-refresh-workers":                      {dropped, "each account is renewed shortly before it expires"},
	"oauth.model-alias":                                    {supported, ""}, // reported in oauthModels
	"oauth.excluded-models":                                {supported, ""}, // reported in oauthModels
	"oauth.settings":                                       {dropped, "needs a model catalog, and no page uses it"},
	"oauth.request-scoped-errors":                          {dropped, "a request-scoped error is already not the account's fault"},
	"oauth.providers.aistudio.ws-auth":                     {dropped, "the AI Studio relay needs a long-lived listener the extension protocol does not have"},
	"oauth.providers.codex.live-media-relay":               {dropped, "realtime and WebRTC are out of scope"},
	"oauth.providers.codex.signin-with-chatgpt-identifier": {supported, ""}, // reported in options
	"oauth.providers.antigravity":                          {dropped, "the Antigravity extension does not read these settings"},
	"oauth.providers.devin":                                {dropped, "rewriting words to get past a content filter is out of scope"},

	"multimedia.disable-image-generation":                     {supported, ""},
	"multimedia.gpt-image-2-base-model":                       {dropped, "it names the model of the Responses-based image path, which is not built; images use the Images API"},
	"multimedia.video-result-auth-cache-ttl":                  {dropped, "it applies to video routes, which are not built"},
	"observability.logs.logging-to-file":                      {supported, ""},
	"observability.logs.logs-max-total-size-mb":               {supported, ""},
	"observability.logs.debug":                                {dropped, "this core writes no debug-level log lines"},
	"observability.logs.request-log":                          {supported, ""},
	"observability.logs.error-logs-max-files":                 {supported, ""},
	"observability.usage.usage-statistics-enabled":            {supported, ""},
	"observability.usage.redis-usage-queue-retention-seconds": {dropped, "reading usage no longer removes it; the record keeps requests by usage.max_events and usage.max_age_ms"},
	"observability.pprof.enable":                              {supported, ""},
	"observability.pprof.addr":                                {supported, ""},
	"models.catalog":                                          {dropped, "this core has no remote model catalog; each extension carries its model list"},
	"models.codex-catalog":                                    {dropped, "this core has no remote model catalog; each extension carries its model list"},
	"models.devin-catalog":                                    {dropped, "this core has no remote model catalog; each extension carries its model list"},
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
	"api-keys.*[].models[].display-name":                       {dropped, "list metadata only, no need found"},
	"api-keys.*[].models[].max-context-length":                 {dropped, "list metadata only, no need found"},
	"api-keys.*[].models[].force-mapping":                      {later, "built when a client needs a reply's model to be the alias it asked for"},
	"api-keys.*[].models[].is-compat":                          {dropped, "a single-provider quirk, no need found"},
	"api-keys.*[].models[].thinking":                           {dropped, "the thinking suffix and extensions cover it"},
	"api-keys.*[].models[].image":                              {dropped, "a Chat or Responses OpenAI-compatible account serves the Images API for any model it lists"},
	"api-keys.*[].models[].input-modalities":                   {dropped, "list metadata only, no need found"},
	"api-keys.*[].models[].output-modalities":                  {dropped, "list metadata only, no need found"},
	"api-keys.*[].models[].use-max-completion-tokens":          {dropped, "a single-provider quirk, no need found"},
	"api-keys.*[].models[].support-configuration-update":       {dropped, "an off-by-default prompt-cache saving for one kind of relay, no need found"},
	"api-keys.*[].models[].openapi":                            {later, "built when someone calls Vertex partner models (Claude, Llama, Mistral) through a key"},
	"api-keys.*[].prefix":                                      {supported, ""},
	"api-keys.*[].excluded-models":                             {supported, ""},
	"api-keys.*[].headers":                                     {supported, ""},
	"api-keys.*[].proxy-url":                                   {supported, ""},
	"api-keys.*[].connection-pool":                             {dropped, "extensions own their connections"},
	"api-keys.*[].request-scoped-errors":                       {dropped, "a request-scoped error is already not the account's fault"},
	"api-keys.*[].disable-cooling":                             {supported, ""},
	"api-keys.*[].request-retry":                               {supported, ""},
	"api-keys.codex[].websockets":                              {supported, ""},
	"api-keys.codex[].disable-codex-cloaking":                  {supported, ""},
	"api-keys.codex[].alpha-search":                            {supported, ""},
	"api-keys.xai[].websockets":                                {dropped, "xAI is served over HTTP, which gives the same replies"},
	"api-keys.vertex[].interactions":                           {supported, ""},
	"api-keys.vertex[].openapi":                                {later, "built when someone calls Vertex partner models (Claude, Llama, Mistral) through a key"},
	"api-keys.vertex[].openapi-location":                       {later, "built when someone calls Vertex partner models (Claude, Llama, Mistral) through a key"},
	"api-keys.openai-compatibility[].disabled":                 {supported, ""},
	"api-keys.openai-compatibility[].support-prompt-cache-key": {dropped, "no need found: a payload rule can set a fixed key"},
	"api-keys.openai-compatibility[].excluded-models":          {dead, ""}, // today's core has no such key there
	"api-keys.claude[].cloak":                                  {dropped, "making other clients look like Claude Code is out of scope"},
	"api-keys.claude[].fingerprint-profile":                    {dropped, "making other clients look like Claude Code is out of scope"},
	"api-keys.claude[].native-tool-search":                     {dropped, "Codex's tool search on Claude accounts: off by default, with no panel or app control"},
	"api-keys.claude[].rebuild-mid-system-message":             {supported, ""},
	"api-keys.claude[].experimental-cch-signing":               {dead, ""},
	"api-keys.meta":                                            {dropped, "the login is built; no need for a bare key"},
}

// aliasLater is the reason an oauth.model-alias entry's setting is not imported yet.
var aliasLater = map[string]string{
	"force-mapping": "built when a client needs a reply's model to be the alias it asked for",
	"openapi":       "built when someone calls Vertex partner models (Claude, Llama, Mistral) through a key",
}

// tree is a decoded YAML mapping.
type tree = map[string]any

// literal is a scalar YAML reads as something other than text (a number, true or false, a
// date): its value as yaml.v3 decodes it into any, and its text as written. Today's core
// decodes its config into typed fields, so a text setting gets the text (api-key: 0123 is
// "0123", not 83) and a number or true-or-false setting gets the value.
type literal struct {
	value any
	text  string
}

// node decodes a YAML value as yaml.v3 decodes it into any, except that a scalar YAML reads
// as something other than text is a literal. yaml.v3 itself resolves aliases and merge keys
// and refuses repeated keys in each mapping.
type node struct{ v any }

func (d *node) UnmarshalYAML(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.MappingNode && stringMap(n):
		var m map[string]node
		if err := n.Decode(&m); err != nil {
			return err
		}
		t := make(tree, len(m))
		for k, e := range m {
			t[k] = e.v
		}
		d.v = t
	case n.Kind == yaml.SequenceNode:
		var list []node
		if err := n.Decode(&list); err != nil {
			return err
		}
		out := make([]any, len(list))
		for i, e := range list {
			out[i] = e.v
		}
		d.v = out
	case n.Kind == yaml.ScalarNode:
		var v any
		if err := n.Decode(&v); err != nil {
			return err
		}
		if _, isText := v.(string); isText || v == nil {
			d.v = v
		} else {
			d.v = literal{value: v, text: n.Value}
		}
	default:
		// A mapping with a key that is not text: as yaml.v3 decodes it into any (which the
		// translation then refuses where it wants a mapping).
		return n.Decode(&d.v)
	}
	return nil
}

// stringMap reports whether yaml.v3 decodes the mapping n into a map with text keys
// (yaml.v3's isStringMap).
func stringMap(n *yaml.Node) bool {
	for i := 0; i < len(n.Content); i += 2 {
		if t := n.Content[i].ShortTag(); t != "!!str" && t != "!!merge" {
			return false
		}
	}
	return true
}

// value returns v, or a literal's value.
func value(v any) any {
	if l, ok := v.(literal); ok {
		return l.value
	}
	return v
}

// plain returns v with each literal below it replaced by its value: v as yaml.v3 decodes it
// into any, for settings that go across as they are.
func plain(v any) any {
	switch v := v.(type) {
	case literal:
		return v.value
	case tree:
		out := make(tree, len(v))
		for k, c := range v {
			out[k] = plain(c)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, c := range v {
			out[i] = plain(c)
		}
		return out
	}
	return v
}

// asText returns a literal's text, and anything else as plain does: a text field of a setting
// that goes across as it is, which this core's own check then reads.
func asText(v any) any {
	if l, ok := v.(literal); ok {
		return l.text
	}
	return plain(v)
}

// each returns the list v with f applied to each item, or v as plain does when it is not a
// list.
func each(v any, f func(any) any) any {
	list, ok := v.([]any)
	if !ok {
		return plain(v)
	}
	out := make([]any, len(list))
	for i, e := range list {
		out[i] = f(e)
	}
	return out
}

// The key names of today's payload types (config.PayloadConfig and the types under it). Today's
// core reads a config.yaml with yaml.v3, which matches a key to a field exactly, so these are
// the only spellings it reads.
var (
	payloadLists     = []string{"default", "default-raw", "override", "override-raw", "filter"}
	payloadRuleKeys  = []string{"models", "params"}
	payloadModelKeys = []string{"name", "protocol", "headers", "from-protocol", "match", "not-match", "exist", "not-exist"}
)

// payloadRules returns payload rules with the field types of today's core
// (config.PayloadConfig): a rule's params, and a model's match and not-match, hold values; a
// model's name, protocol, from-protocol, headers, exist and not-exist, and a filter rule's
// params, are text as written. Anything else goes across as plain does.
//
// It carries the rules as today's core applied them, which is not always as written, and names
// each difference in a notice:
//   - Today's core matches a key to its field exactly and drops any other key, so a key in
//     another letter case (Override:, Name:) never took effect there. It is dropped here too:
//     the JSON config is read with encoding/json, which would match it case-insensitively.
//   - A -raw rule with a string param that is not JSON is dropped as a whole (a warning in
//     today's core's log); a -raw param that is null or that json.Marshal refuses (.inf, .nan)
//     was skipped, and a date is sent as the JSON string of its date (paramValue).
//   - A param of default or override whose value json.Marshal refuses was skipped, or written
//     into the body as the invalid text +Inf, NaN or -Inf; it is dropped here.
//   - A model whose match holds such a value never matched, so it is dropped; a not-match
//     condition with one never equalled anything, so it never excluded a request, and is dropped.
//
// A value JSON cannot hold elsewhere in the rules fails the load, naming the rules.
func (x *translator) payloadRules(rules tree) (tree, error) {
	const base = "requests.payload"
	var ignored, dropped []string
	// keep returns the entries of m whose key is one of known, and records the others in
	// ignored: a key that differs from a known one only in letter case with the spelling to use,
	// any other as unknown.
	keep := func(where string, m tree, known []string) tree {
		out := tree{}
		for _, k := range slices.Sorted(maps.Keys(m)) {
			switch i := slices.IndexFunc(known, func(n string) bool { return strings.EqualFold(n, k) }); {
			case i >= 0 && known[i] == k:
				out[k] = m[k]
			case i >= 0:
				ignored = append(ignored, fmt.Sprintf("%s.%s (today's core reads %s)", where, k, known[i]))
			default:
				ignored = append(ignored, fmt.Sprintf("%s.%s (not a payload key)", where, k))
			}
		}
		return out
	}
	drop := func(where, why string) { dropped = append(dropped, where+": "+why) }
	// items applies f to each item of the list v and keeps the results f accepts; a value that
	// is not a list goes across as plain does.
	items := func(v any, f func(i int, e any) (any, bool)) any {
		list, ok := v.([]any)
		if !ok {
			return plain(v)
		}
		out := make([]any, 0, len(list))
		for i, e := range list {
			if r, keepIt := f(i, e); keepIt {
				out = append(out, r)
			}
		}
		return out
	}
	// model carries one entry of a rule's models, or reports false for one that never matched.
	model := func(where string) func(int, any) (any, bool) {
		return func(i int, v any) (any, bool) {
			at := fmt.Sprintf("%s[%d]", where, i)
			m, ok := v.(tree)
			if !ok {
				return plain(v), true
			}
			kept := keep(at, m, payloadModelKeys)
			out := tree{}
			for _, k := range slices.Sorted(maps.Keys(kept)) {
				f := kept[k]
				switch h, isMap := f.(tree); {
				case k == "name", k == "protocol", k == "from-protocol":
					out[k] = asText(f)
				case k == "exist", k == "not-exist":
					out[k] = each(f, asText)
				case k == "headers" && isMap:
					hs := tree{}
					for name, val := range h {
						hs[name] = asText(val)
					}
					out[k] = hs
				case k == "match":
					for c, cond := range conditions(f) {
						for _, path := range slices.Sorted(maps.Keys(cond)) {
							if why := notJSON(cond[path]); why != "" {
								drop(at, fmt.Sprintf("match[%d][%q] is not JSON (%s), model dropped: today's core never matched it", c, path, why))
								return nil, false
							}
						}
					}
					out[k] = plain(f)
				case k == "not-match":
					conds, isList := f.([]any)
					if !isList {
						out[k] = plain(f)
						break
					}
					kept := make([]any, 0, len(conds))
					for c, e := range conds {
						cond, isCond := e.(tree)
						if !isCond {
							kept = append(kept, plain(e))
							continue
						}
						nm := tree{}
						for _, path := range slices.Sorted(maps.Keys(cond)) {
							if why := notJSON(cond[path]); why != "" {
								drop(at, fmt.Sprintf("not-match[%d][%q] is not JSON (%s), condition dropped: it never equalled a value in today's core", c, path, why))
								continue
							}
							nm[path] = plain(cond[path])
						}
						kept = append(kept, nm)
					}
					out[k] = kept
				default:
					out[k] = plain(f)
				}
			}
			return out, true
		}
	}
	// rule carries one rule of a list, or reports false for one today's core dropped.
	rule := func(list string) func(int, any) (any, bool) {
		raw := strings.HasSuffix(list, "-raw")
		return func(i int, e any) (any, bool) {
			at := fmt.Sprintf("%s.%s[%d]", base, list, i)
			r, ok := e.(tree)
			if !ok {
				return plain(e), true
			}
			kept := keep(at, r, payloadRuleKeys)
			params, _ := kept["params"].(tree)
			if raw {
				// Today's core drops the whole rule for a string param that is not JSON text.
				for _, name := range slices.Sorted(maps.Keys(params)) {
					if s, isText := plain(params[name]).(string); isText && !rawText(s) {
						drop(at, fmt.Sprintf("param %q is a string that is not JSON, rule dropped: today's core dropped the whole rule and logged a warning", name))
						return nil, false
					}
				}
			}
			o := tree{}
			for _, k := range slices.Sorted(maps.Keys(kept)) {
				f := kept[k]
				switch {
				case k == "models":
					o[k] = items(f, model(at+".models"))
				case k == "params" && list == "filter":
					o[k] = each(f, asText)
				case k == "params" && params != nil:
					ps := tree{}
					for _, name := range slices.Sorted(maps.Keys(params)) {
						v, why := paramValue(raw, params[name])
						if why != "" {
							drop(fmt.Sprintf("%s.params[%q]", at, name), why)
							continue
						}
						ps[name] = v
					}
					o[k] = ps
				default:
					o[k] = plain(f)
				}
			}
			return o, true
		}
	}
	out := tree{}
	lists := keep(base, rules, payloadLists)
	for _, list := range slices.Sorted(maps.Keys(lists)) {
		out[list] = items(lists[list], rule(list))
	}
	if len(ignored) > 0 {
		x.notes = append(x.notes, "config.yaml: payload keys ignored, as today's core ignores them (it matches key names exactly): "+strings.Join(dedupe(ignored), ", "))
	}
	for _, d := range dedupe(dropped) {
		x.notes = append(x.notes, "config.yaml: "+d)
	}
	if _, err := json.Marshal(out); err != nil {
		return nil, fmt.Errorf("requests.payload: a value is not JSON where today's core reads another type: %w", err)
	}
	return out, nil
}

// conditions returns the maps of a match or not-match list, as today's core reads them; any
// other shape has none.
func conditions(v any) []tree {
	var out []tree
	list, _ := v.([]any)
	for _, e := range list {
		if m, ok := e.(tree); ok {
			out = append(out, m)
		}
	}
	return out
}

// notJSON returns why json.Marshal refuses the value v, as today's core's payload code calls it,
// or "" when it takes it. A value it refuses is .inf or .nan, alone or inside a list or mapping.
func notJSON(v any) string {
	if _, err := json.Marshal(plain(v)); err != nil {
		return err.Error()
	}
	return ""
}

// rawText reports whether the string s is JSON text a -raw rule can hold: today's core drops a
// rule whose string param is empty or not valid JSON once its spaces are trimmed.
func rawText(s string) bool {
	t := bytes.TrimSpace([]byte(s))
	return len(t) > 0 && json.Valid(t)
}

// paramValue returns what a rule's param holds in this core's config, or why the param is
// dropped, as today's core applied it.
//
// In a -raw rule (raw) the value is sent as JSON text. A string is that text and goes across as
// it is (the caller has dropped the rule if it is not JSON). Any other value is what json.Marshal
// makes of it: a date YAML reads as one (2024-01-15) is a time.Time there, which json.Marshal
// writes as the JSON string "2024-01-15T00:00:00Z", so that text is returned. A param that is
// null or that json.Marshal refuses (.inf and .nan, also inside a list or mapping) was skipped,
// and the rule's other params still applied.
//
// In any other rule the value is written as a JSON value. When json.Marshal refuses it, today's
// core either skipped the param or, for a top-level .inf, .nan or -.inf in default (and -.inf in
// override), wrote the text +Inf, NaN or -Inf into the body, which no upstream accepts. Either
// way there is no value to carry.
func paramValue(raw bool, v any) (any, string) {
	v = plain(v)
	if raw {
		switch v := v.(type) {
		case nil:
			return nil, "null, dropped: today's core skipped the param"
		case string:
			return v, ""
		case time.Time:
			b, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Sprintf("not JSON (%v), dropped: today's core skipped the param", err)
			}
			return string(b), ""
		}
	}
	if why := notJSON(v); why != "" {
		if raw {
			return nil, fmt.Sprintf("not JSON (%s), dropped: today's core skipped the param", why)
		}
		return nil, fmt.Sprintf("not JSON (%s), dropped: today's core skipped the param or wrote invalid JSON into the body", why)
	}
	return v, ""
}

// Translate turns the contents of a config.yaml written for today's core into this core's
// config. It fails only where today's core would refuse the file too (it is not a mapping, a
// key appears twice, config-version is not 8) or a value has the wrong type.
func Translate(b []byte) (*Result, error) { return translateFile(b, "", false) }

// ImportConfig turns a config.yaml written for today's core into this core's config for an import
// (pluxy import cpa), as Translate does, with the import's own changes: no server.host listens on
// 127.0.0.1, the management key is not brought (pluxy keeps its own), the example config's client
// keys are left out, this core's own settings that belong to the config core already runs on
// (credentials_dir and the like) are not brought, and relative TLS file paths are taken from
// dir, the file's folder. Result.Ledger then names every key of the file in a report row; it
// fails when it does not account for one.
func ImportConfig(b []byte, dir string) (*Result, error) {
	res, err := translateFile(b, dir, true)
	if err != nil {
		return nil, err
	}
	if err := res.Ledger.Check(); err != nil {
		return nil, err
	}
	return res, nil
}

func translateFile(b []byte, dir string, importing bool) (*Result, error) {
	// The first decode refuses what yaml.v3 refuses (repeated keys, an alias that contains
	// itself, excessive aliasing) with its own errors, before the second, which decodes each
	// mapping and list on its own and so does not see those limits.
	var check any
	if err := yaml.Unmarshal(b, &check); err != nil {
		return nil, err
	}
	var doc node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	root := doc.v
	t, ok := root.(tree)
	if !ok {
		if root == nil {
			t = tree{}
		} else {
			return nil, errors.New("the config must be a mapping")
		}
	}
	x := &translator{t: t, out: map[string]any{}, res: &Result{Import: DefaultImportOptions, Ledger: &Ledger{}, Accounts: map[string]string{}},
		importing: importing, dir: dir, origin: map[string]string{}, whyFor: map[string][]string{}}
	leaves("", t, func(p string) { x.res.Ledger.Expect(ConfigItem(p)) })
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
	item                                string   // the provider group being read, as its v8 path
	notes                               []string // notices about one setting each

	importing bool   // ImportConfig
	dir       string // the file's folder, in an import
	// origin maps the path of a key moved to its v8 place (see moves) to its path in the file.
	origin map[string]string
	// why, when not nil, collects what a provider group's notes say, for its report row.
	why *[]string
	// whyFor collects, by the v8 path of a report item, what the chains say about it.
	whyFor map[string][]string
	// pending are rows of provider groups and alias channels, added once the chains are known.
	pending []Entry
	pools   []pool
	served  []served
}

// pool is an alias of an openai-compatibility key whose models today's core took in turn.
type pool struct {
	name, ext, account, item string
	models                   []string
}

// served is the model names clients use that a provider serves through the accounts of one
// item of the report (a provider group, or an alias channel's sign-in accounts).
type served struct {
	ext, account, item string
	names              []string
}

// fileKey is the path in the file of the key at path.
func (x *translator) fileKey(path string) string {
	if o, ok := x.origin[path]; ok {
		return o
	}
	return path
}

// mark adds a report row to list for the key at path.
func (x *translator) mark(list int, path string, row Row) {
	k := x.fileKey(path)
	if row.What == "" {
		row.What = k
	}
	x.res.Ledger.Add(list, ConfigItem(k), row)
}

// markAll adds row to list for each item of the value v at path.
func (x *translator) markAll(list int, path string, v any, row Row) {
	leaves(path, v, func(p string) { x.mark(list, p, row) })
}

// fill adds a brought row for each item of the value v at path that has none yet: a setting the
// translation read.
func (x *translator) fill(path string, v any) {
	leaves(path, v, func(p string) {
		if !x.res.Ledger.Has(ConfigItem(x.fileKey(p))) {
			x.mark(Brought, p, Row{})
		}
	})
}

// note adds a notice, and its why to the provider group being read.
func (x *translator) note(notice, why string) {
	x.notes = append(x.notes, notice)
	if x.why != nil {
		*x.why = append(*x.why, why)
	}
}

// move moves the value at from to to, as today's core reads its older spellings: when to is set
// too, the v8 value wins, field by field. Each item of the value keeps its path in the file
// (origin), and one that loses to the v8 value is named as not brought.
func (x *translator) move(from, to string) {
	v, ok := get(x.t, from)
	if !ok {
		return
	}
	cur, has := get(x.t, to)
	leaves(from, v, func(p string) {
		rest := trail(p, from)
		if has && occupied(cur, rest) {
			x.mark(NotBrought, p, Row{Why: "the file sets " + display(to+rest) + " too, and today's core reads that one"})
			return
		}
		x.origin[to+rest] = x.fileKey(p)
	})
	if has {
		set(x.t, to, merge(cur, v))
	} else {
		set(x.t, to, v)
	}
	del(x.t, from)
}

// occupied reports whether merge keeps cur's value at the path rest below it (".a.b", or ""
// for cur itself) over another's.
func occupied(cur any, rest string) bool {
	if rest == "" {
		return true
	}
	m, ok := cur.(tree)
	if !ok {
		return true
	}
	k, more, _ := strings.Cut(rest[1:], ".")
	c, has := m[k]
	if !has {
		return false
	}
	if more != "" {
		more = "." + more
	}
	return occupied(c, more)
}

func (x *translator) run() error {
	if v, ok := get(x.t, "config-version"); ok {
		if n, isInt := value(v).(int); !isInt || n != 8 {
			return errors.New("unsupported config-version (expected 8)")
		}
	}
	if v, ok := x.t["config-version"]; ok {
		x.mark(NotBrought, "config-version", Row{Why: "pluxy's config has no version number; the file is read as version 8"})
		delete(x.t, "config-version")
		_ = v
	}
	for _, k := range newKeys {
		if v, ok := x.t[k]; ok {
			// In an import, core keeps its own folders and its own extension under development.
			if x.importing && !slices.Contains([]string{"extensions", "cors", "drop_policy", "token_count_estimate"}, k) {
				x.mark(NotBrought, k, Row{Why: "core keeps its own " + k})
			} else {
				x.out[k] = plain(v)
				x.mark(Brought, k, Row{As: k})
			}
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
			x.origin["access.api-keys"] = "api-keys"
		} else {
			x.mark(NotBrought, "api-keys", Row{Why: "the file sets access.api-keys too, and today's core reads that one"})
		}
		delete(x.t, "api-keys")
	case tree:
		providers = v
		delete(x.t, "api-keys")
	case nil:
		if _, ok := x.t["api-keys"]; ok {
			x.mark(NotBrought, "api-keys", Row{Why: "empty"})
		}
		delete(x.t, "api-keys")
	default:
		return errors.New("api-keys must be a list of client keys or a mapping of provider sections")
	}
	for _, m := range moves {
		x.move(m[0], m[1])
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
	if err := x.chains(); err != nil {
		return err
	}
	for _, e := range x.pending {
		path := e.Item
		if why := x.whyFor[path]; len(why) > 0 {
			if e.List == Brought {
				e.List = Changed
			}
			e.Row.Why = joinWhy(append(splitWhy(e.Row.Why), why...))
		}
		x.mark(e.List, path, e.Row)
	}
	x.classify("", x.t, nil, x.markRule)

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
	"upstream.xai.inject-x-search", "requests.proxy-url", "requests.payload",
	"oauth.providers.codex.signin-with-chatgpt-identifier",
	"multimedia.disable-image-generation", "oauth.auth-dir", "observability.usage.usage-statistics-enabled",
	"observability.logs.logging-to-file", "observability.logs.logs-max-total-size-mb",
	"observability.logs.request-log", "observability.logs.error-logs-max-files",
	"observability.pprof.enable", "observability.pprof.addr",
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

// managementKeyWhy is why an import does not bring the file's management key: it guards every
// client of core, so the owner changes core's own with pluxy's command instead.
const managementKeyWhy = "pluxy keeps its own management key: it guards every client of core, and an import does not change it; change it with POST /v0/management/management-key/rotate"

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
	host = strings.TrimSpace(host)
	x.res.OldListen = net.JoinHostPort(cmp.Or(host, "0.0.0.0"), strconv.Itoa(port))
	if x.importing && host == "" {
		// Today's core listens on every interface then; pluxy only on this machine (T6).
		from := "(not set: every interface)"
		if _, set := get(x.t, "server.host"); set {
			from = "\"\" (every interface)"
		}
		host = "127.0.0.1"
		x.mark(Changed, "server.host", Row{From: from, To: host,
			Why:      "pluxy listens on this machine only unless told otherwise",
			KeepWith: fmt.Sprintf(`"listen": %q in pluxy's config file, then restart core`, x.res.OldListen)})
		if _, set := get(x.t, "server.host"); !set {
			// The row stands for a key the file does not set; an import into a running core
			// corrects it with the rest of the listen rows (importRows).
			x.res.ListenKeys = append(x.res.ListenKeys, ConfigItem(x.fileKey("server.host")))
		}
	}
	x.out["listen"] = net.JoinHostPort(host, strconv.Itoa(port))
	for _, p := range []string{"server.host", "server.port"} {
		if _, ok := get(x.t, p); ok {
			x.res.ListenKeys = append(x.res.ListenKeys, ConfigItem(x.fileKey(p)))
		}
	}

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
		cert, key = strings.TrimSpace(cert), strings.TrimSpace(key)
		if x.importing {
			// Relative to the file's folder, as core reads them relative to its own config's.
			for _, p := range []*string{&cert, &key} {
				if *p != "" && !filepath.IsAbs(*p) {
					*p = filepath.Join(x.dir, *p)
				}
			}
		}
		x.out["tls"] = map[string]any{"enable": true, "cert": cert, "key": key}
	} else {
		// TLS off, as today's core used it: a certificate and key are not used then either.
		for _, p := range []string{"server.tls.cert", "server.tls.key"} {
			if _, set := get(x.t, p); set {
				x.mark(NotBrought, p, Row{Why: "it does nothing while server.tls.enable is off, so nothing is lost"})
			}
		}
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
		var examples []string
		if x.importing {
			// The example config's keys are public: core refuses every request while one is set.
			keys = slices.DeleteFunc(keys, func(k string) bool {
				if slices.Contains(ExampleClientKeys, k) {
					examples = append(examples, k)
					return true
				}
				return false
			})
		}
		as := fmt.Sprintf("%d client keys", len(keys))
		if len(keys) == 1 {
			as = "1 client key"
		}
		if len(examples) > 0 {
			x.mark(Changed, "access.api-keys", Row{As: as, Why: "left out the example config's public keys " + quoteAll(examples) + ", which anyone can read"})
		} else if x.importing && len(keys) == 0 {
			// An import adds keys; it never takes core's away.
			x.mark(NotBrought, "access.api-keys", Row{Why: "it lists no client keys, and an import keeps the ones core has"})
		} else {
			x.mark(Brought, "access.api-keys", Row{As: as})
		}
		if len(keys) > 0 || !x.importing {
			x.out["client_keys"] = keys
		}
	}

	secret, err := str(x.t, "management.secret-key")
	if err != nil {
		return err
	}
	// The key is used as written, as today's core uses it: a space in it is part of it.
	switch {
	case x.importing && secret != "":
		// An import never brings the key, plain or hashed: the key guards every client of core,
		// the importing one included, so an import does not change who may manage it.
		x.res.HashedManagementKey = bcryptHash.MatchString(secret)
		x.mark(NotBrought, "management.secret-key", Row{Why: managementKeyWhy})
	case bcryptHash.MatchString(secret):
		x.res.HashedManagementKey = true
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
	case "soonest-reset":
		r["strategy"] = "soonest-reset"
	default:
		// Today's core uses round-robin for a name it does not know.
		x.notes = append(x.notes, fmt.Sprintf("config.yaml: routing.strategy %q is not known: using round-robin", s))
		x.mark(Changed, "routing.strategy", Row{From: s, To: "round-robin", Why: "today's core does not know the name either, and uses round-robin"})
	}
	if on, err := flag(x.t, "routing.session-affinity"); err != nil {
		return err
	} else if on {
		ttl := time.Hour
		_, ttlSet := get(x.t, "routing.session-affinity-ttl")
		if s, err := str(x.t, "routing.session-affinity-ttl"); err != nil {
			return err
		} else if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil && d > 0 {
			ttl = max(d, time.Second) // as today's core reads it
		}
		aff := map[string]any{}
		// An import sets the TTL only when the file does: the one-hour default is not the file's
		// setting, and core's own TTL otherwise stays.
		if ttlSet || !x.importing {
			aff["ttl_ms"] = ttl.Milliseconds()
		}
		if x.importing {
			// Core's own config may have it off, which a JSON config otherwise is on without.
			aff["enabled"] = true
		}
		r["session_affinity"] = aff
	} else if _, set := get(x.t, "routing.session-affinity"); set || !x.importing {
		// Today's core has it off unless the file turns it on; a JSON config has it on unless
		// it says otherwise. An import leaves core's own setting alone unless the file sets it.
		r["session_affinity"] = map[string]any{"enabled": false}
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
	// waits), whatever its example config shows. An import leaves core's own count alone unless
	// the file sets it.
	retry, err := integer(x.t, "routing.retry.request-retry")
	if err != nil {
		return err
	}
	if _, set := get(x.t, "routing.retry.request-retry"); set || !x.importing {
		r["request_retry"] = max(retry, 0)
	}
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
	if on, err := flag(x.t, "upstream.xai.inject-x-search"); err != nil {
		return err
	} else if on {
		x.res.Import.InjectXSearch = true
	}
	switch v, _ := get(x.t, "multimedia.disable-image-generation"); value(v) {
	case nil, false, "false", "":
	case true, "true":
		x.res.Import.ImageGenerationTool = false
		x.res.Import.Images = false
	case "chat", "passthrough":
		x.res.Import.ImageGenerationTool = false
	default:
		return fmt.Errorf("multimedia.disable-image-generation must be false, true, \"chat\" or \"passthrough\"")
	}
	if on, err := flag(x.t, "upstream.claude.model-level-cooling"); err != nil {
		return err
	} else if on {
		x.res.Import.ClaudeModelLevelCooling = true
	}
	if v, ok := get(x.t, "upstream.codex.websocket-compression"); ok && v != nil {
		if on, err := flag(x.t, "upstream.codex.websocket-compression"); err != nil {
			return err
		} else if !on {
			x.notes = append(x.notes, "config.yaml: upstream.codex.websocket-compression: false is not supported: compression stays on")
			x.mark(Changed, "upstream.codex.websocket-compression", Row{From: "false", To: "true", Why: "pluxy always compresses Codex WebSocket messages"})
		}
	}
	id, err := str(x.t, "oauth.providers.codex.signin-with-chatgpt-identifier")
	if err != nil {
		return err
	}
	x.res.Import.SIWCIdentifier = strings.TrimSpace(id)
	dir, err := str(x.t, "oauth.auth-dir")
	if err != nil {
		return err
	}
	x.res.AuthDir = strings.TrimSpace(dir)
	proxy, err := str(x.t, "requests.proxy-url")
	if err != nil {
		return err
	}
	if proxy = strings.TrimSpace(proxy); proxy != "" {
		if err := proxyurl.Check("requests.proxy-url", proxy); err != nil {
			return err
		}
		x.out["proxy_url"] = proxy
	}
	// The payload rules keep today's names, so they go across under those names, their text
	// fields as written, a key spelled in another letter case dropped as today's core drops it,
	// and a -raw param as today's core sent it (payloadRules); the JSON config's own check
	// (payload.Validate) refuses what this core cannot apply.
	if rules, _ := get(x.t, "requests.payload"); rules != nil {
		m, isMap := rules.(tree)
		if !isMap {
			return errors.New("requests.payload must be a mapping")
		}
		n := len(x.notes)
		payload, err := x.payloadRules(m)
		if err != nil {
			return err
		}
		x.out["payload"] = payload
		if len(x.notes) > n {
			var why []string
			for _, s := range x.notes[n:] {
				why = append(why, strings.TrimPrefix(s, "config.yaml: "))
			}
			x.mark(Changed, "requests.payload", Row{As: "payload", Why: joinWhy(why)})
		}
	}
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
	// The profiling pages' switch and address (today's pprof.enable and pprof.addr). Off, the
	// address asks for nothing; the default address is core's own (server.defaultPprofAddr).
	if on, err := flag(x.t, "observability.pprof.enable"); err != nil {
		return err
	} else if on {
		pprof := tree{"enable": true}
		addr, err := str(x.t, "observability.pprof.addr")
		if err != nil {
			return err
		}
		if addr != "" && addr != "127.0.0.1:8316" {
			pprof["addr"] = addr
		}
		x.out["pprof"] = pprof
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
		path := "api-keys." + f.v8
		v8v, inV8 := v8[f.v8]
		oldv, inOld := x.t[f.old]
		chosen := "" // the list read: path or f.old
		if inV8 && v8v != nil {
			chosen = path
			list, isList := v8v.([]any)
			if !isList {
				return fmt.Errorf("api-keys.%s must be a list", f.v8)
			}
			groups = list
			if inOld {
				x.markAll(NotBrought, f.old, oldv, Row{Why: "the file sets " + path + " too, and today's core reads that one"})
			}
		} else if inOld && oldv != nil {
			list, isList := oldv.([]any)
			if !isList {
				return fmt.Errorf("%s must be a list", f.old)
			}
			chosen = f.old
			for i, e := range list {
				g, isMap := e.(tree)
				if !isMap {
					return fmt.Errorf("%s entries must be mappings", f.old)
				}
				groups = append(groups, oldGroup(g, f.v8 == "openai-compatibility"))
				x.origin[fmt.Sprintf("%s[%d]", path, i)] = fmt.Sprintf("%s[%d]", f.old, i)
			}
			if inV8 {
				x.mark(NotBrought, path, Row{Why: "empty"})
			}
		} else {
			// Present but empty.
			if inV8 {
				x.markAll(NotBrought, path, v8v, Row{Why: "empty"})
			}
			if inOld {
				x.markAll(NotBrought, f.old, oldv, Row{Why: "empty"})
			}
		}
		delete(v8, f.v8)
		delete(x.t, f.old)
		if len(groups) == 0 {
			if chosen != "" {
				x.mark(NotBrought, chosen, Row{Why: "empty"})
			}
			continue
		}
		if f.extension == "" {
			x.classify(path, groups, nil, x.markRule)
			continue
		}
		n := 0
		for gi, gv := range groups {
			g, ok := gv.(tree)
			if !ok {
				return fmt.Errorf("%s[%d] must be a mapping", path, gi)
			}
			gpath := fmt.Sprintf("%s[%d]", path, gi)
			var why, made []string
			x.why = &why
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
			if len(keys) == 0 && !disabled {
				// Every section: an upstream that needs no key is not supported yet.
				x.keyless(path)
				why = append(why, "it has no keys (an upstream that needs no key is not supported yet)")
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
					why = append(why, fmt.Sprintf("keys[%d] has no api-key (an upstream that needs no key is not supported yet)", ki))
					continue
				}
				id := f.old + "-" + strconv.Itoa(n)
				if name != "" {
					id = name
					if len(keys) > 1 {
						id = name + "-" + strconv.Itoa(ki+1)
					}
				}
				x.item = gpath
				a, err := x.account(f, g, k, accountID(id, used), path)
				if err != nil {
					return err
				}
				// Today's core keeps no Codex API key without a base-url (SanitizeCodexKeys); the
				// key is dropped and named in the log. A key added by a login, which asks for no
				// base_url, still gets the extension's default.
				if st, _ := a["settings"].(map[string]any); f.v8 == "codex" && st["base_url"] == nil {
					x.note(fmt.Sprintf("config.yaml: %s key %s has no base-url: dropped, as today's core drops a Codex API key without one", display(path), id),
						fmt.Sprintf("keys[%d] has no base-url: today's core drops a Codex API key without one too", ki))
					continue
				}
				accounts[f.extension] = append(accounts[f.extension], a)
				aid := a["id"].(string)
				made = append(made, aid)
				x.res.Accounts[aid] = ConfigItem(x.fileKey(gpath))
			}
			// The key list is read; classify the rest of the group and each key's own fields.
			rest := tree{}
			for k, v := range g {
				if k != "keys" {
					rest[k] = v
				}
			}
			collect := func(p string, v any, status int, note string, quiet bool) {
				if quiet || status == supported {
					return
				}
				field := display(trail(p, path+"[]")[1:])
				if status == partly {
					why = append(why, field+": "+note+" not brought")
					return
				}
				why = append(why, field+" not brought ("+cmp.Or(note, statusWhy(status))+")")
			}
			x.classify(path+"[]", rest, nil, collect)
			for _, kv := range keys {
				x.classify(path+"[]", kv, nil, collect)
			}
			x.why = nil
			row := Row{Name: name}
			list := Brought
			switch {
			case disabled:
				list, why = NotBrought, []string{"disabled: true, so today's core did not use it either"}
			case len(made) == 0:
				list = NotBrought
			default:
				row.As = f.extension + " " + plural(len(made), "account", "accounts") + " " + strings.Join(made, ", ")
				if len(why) > 0 {
					list = Changed
				}
			}
			row.Why = joinWhy(why)
			x.pending = append(x.pending, Entry{List: list, Item: gpath, Row: row})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(v8)) {
		x.unknownKeys = append(x.unknownKeys, "api-keys."+k)
		x.markAll(NotBrought, "api-keys."+k, v8[k], Row{Why: statusWhy(unknown)})
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
		n, ok := value(p).(int)
		if !ok {
			return nil, fmt.Errorf("%s.priority must be a whole number", path)
		}
		a["priority"] = n
	}
	if d := field(g, k, "disable-cooling"); d != nil {
		on, ok := value(d).(bool)
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
		n, ok := value(rr).(int)
		if !ok {
			return nil, fmt.Errorf("%s.request-retry must be a whole number", path)
		}
		if n >= 0 { // negative inherits routing's count, as in today's core
			a["request_retry"] = n
		}
	}
	if w := k["weight"]; w != nil {
		n, ok := value(w).(int)
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
		public := map[string]string{} // the name clients use, to the upstream name kept for it
		var pooled []string           // the names clients use that later entries repeat, in order
		dropped := map[string][]string{}
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
			// The name clients use is the alias, else the name; the first entry for a name wins.
			// Today's core does the same in every section but openai-compatibility, where
			// entries that share an alias are a pool its requests rotate over and fall back
			// through; this core makes the pool a fallback chain (chains).
			name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
			if name == "" {
				continue
			}
			pub := cmp.Or(alias, name)
			if kept, seen := public[pub]; seen {
				if f.v8 == "openai-compatibility" && kept != name && !slices.Contains(dropped[pub], name) {
					if dropped[pub] == nil {
						pooled = append(pooled, pub)
					}
					dropped[pub] = append(dropped[pub], name)
				}
				continue
			}
			public[pub] = name
			if alias == "" || alias == name {
				models = append(models, name)
			} else {
				models = append(models, map[string]any{"name": name, "alias": alias})
			}
		}
		var names []string // the names clients use
		for pub := range public {
			names = append(names, pub)
		}
		slices.Sort(names)
		for _, pub := range pooled {
			members := append([]string{public[pub]}, dropped[pub]...)
			x.notes = append(x.notes, fmt.Sprintf("config.yaml: %s key %s: models: the alias %q lists %d upstream models, which today's core rotates over as a pool; this core tries them in order as the fallback chain routing.chains.%s",
				display(path), id, pub, len(members), pub))
			x.pools = append(x.pools, pool{name: pub, ext: f.extension, account: id, item: x.item, models: members})
			// Each model of the pool is listed under its own name, which the chain's steps name.
			models = slices.DeleteFunc(models, func(m any) bool {
				e, isMap := m.(map[string]any)
				return isMap && e["alias"] == pub || m == pub
			})
			for _, n := range members {
				if !slices.Contains(models, any(n)) {
					models = append(models, n)
				}
			}
		}
		if len(models) > 0 {
			a["models"] = models
		}
		x.served = append(x.served, served{ext: f.extension, account: id, item: x.item, names: names})
	}
	if p, err := scalar(field(g, k, "prefix"), path+".prefix"); err != nil {
		return nil, err
	} else if p = modelPrefix(p); p != "" {
		a["prefix"] = p
	}
	if p, err := scalar(field(g, k, "proxy-url"), path+".proxy-url"); err != nil {
		return nil, err
	} else if p = strings.TrimSpace(p); p != "" {
		if err := proxyurl.Check(display(path)+" key "+id+" proxy-url", p); err != nil {
			return nil, err
		}
		a["proxy_url"] = p
	}
	// A key's headers replace its group's; an openai-compatibility group's are its keys', which
	// have none of their own there.
	hv := field(g, k, "headers")
	if f.v8 == "openai-compatibility" {
		hv = g["headers"]
	}
	if h, left, err := headers(hv, path+".headers"); err != nil {
		return nil, err
	} else if len(h) > 0 || len(left) > 0 {
		if err := upheader.Check(display(path)+" key "+id+" headers", h); err != nil {
			return nil, err
		}
		if len(h) > 0 {
			a["headers"] = h
		}
		for _, l := range left {
			x.note(fmt.Sprintf("config.yaml: %s key %s header %s not imported: %s", display(path), id, l.Name, l.Why),
				"header "+l.Name+" not brought ("+l.Why+")")
		}
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
		if !x.res.Import.Images {
			settings["images"] = false
		}
	case "claude":
		rebuild, err := boolean_(field(g, k, "rebuild-mid-system-message"), path+".rebuild-mid-system-message")
		if err != nil {
			return nil, err
		}
		if rebuild {
			settings["rebuild_mid_system_message"] = true
		}
		if x.res.Import.ClaudeModelLevelCooling {
			settings["model_level_cooling"] = true
		}
	case "xai":
		if x.res.Import.InjectXSearch {
			settings["inject_x_search"] = true
		}
	case "vertex":
		settings["vertex"] = map[string]any{}
		on, err := boolean_(field(g, k, "interactions"), path+".interactions")
		if err != nil {
			return nil, err
		}
		if on {
			settings["interactions"] = true
		}
	case "interactions":
		settings["interactions"] = true
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
		v, present := get(x.t, path)
		del(x.t, path)
		if v == nil {
			if present {
				x.mark(NotBrought, path, Row{Why: "empty"})
			}
			return nil, nil, nil
		}
		m, ok := v.(tree)
		if !ok {
			return nil, nil, fmt.Errorf("%s must be a mapping of channels", path)
		}
		if len(m) == 0 {
			x.mark(NotBrought, path, Row{Why: "empty"})
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
		var why []string
		item := "oauth.model-alias." + ch
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
						x.laterKeys = append(x.laterKeys, "oauth.model-alias."+k+" ("+aliasLater[k]+")")
						why = append(why, k+" not brought (not supported yet: "+aliasLater[k]+")")
					}
				default:
					x.unknownKeys = append(x.unknownKeys, "oauth.model-alias."+k)
					why = append(why, k+" not brought ("+statusWhy(unknown)+")")
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
			x.mark(NotBrought, item, Row{Why: "no entry maps a name to another, so it asks for nothing"})
			continue
		}
		pkg, ok := channels[strings.ToLower(strings.TrimSpace(ch))]
		if !ok {
			x.notes = append(x.notes, fmt.Sprintf("config.yaml: oauth.model-alias.%s: no extension serves the %s accounts of today's core; ignored", ch, ch))
			x.mark(NotBrought, item, Row{Why: "no pluxy extension serves the " + ch + " accounts of today's core"})
			continue
		}
		e, err := section(pkg)
		if err != nil {
			return err
		}
		have, _ := e["model_alias"].([]any)
		e["model_alias"] = append(have, rules...)
		var names []string
		for _, r := range rules {
			names = append(names, r.(map[string]any)["alias"].(string))
		}
		x.served = append(x.served, served{ext: pkg, item: item, names: names})
		l := Brought
		if len(why) > 0 {
			l = Changed
		}
		x.pending = append(x.pending, Entry{List: l, Item: item, Row: Row{As: pkg + " model_alias (" + plural(len(rules), "1 alias", fmt.Sprintf("%d aliases", len(rules))) + ") for accounts added by sign-in", Why: joinWhy(why)}})
	}

	excluded, names, err := read("oauth.excluded-models")
	if err != nil {
		return err
	}
	for _, ch := range names {
		item := "oauth.excluded-models." + ch
		if excluded[ch] == nil {
			x.mark(NotBrought, item, Row{Why: "empty"})
			continue
		}
		list, err := stringList(excluded[ch], "oauth.excluded-models."+ch)
		if err != nil {
			return err
		}
		pats := patterns(list)
		if len(pats) == 0 {
			x.mark(NotBrought, item, Row{Why: "empty"})
			continue
		}
		pkg, ok := channels[strings.ToLower(strings.TrimSpace(ch))]
		if !ok {
			x.notes = append(x.notes, fmt.Sprintf("config.yaml: oauth.excluded-models.%s: no extension serves the %s accounts of today's core; ignored", ch, ch))
			x.mark(NotBrought, item, Row{Why: "no pluxy extension serves the " + ch + " accounts of today's core"})
			continue
		}
		x.mark(Brought, item, Row{As: pkg + " excluded_models for accounts added by sign-in"})
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
	"oauth.providers.antigravity.antigravity-credits":         true,
	"oauth.providers.antigravity.signature-cache-enabled":     true,
	"multimedia.gpt-image-2-base-model":                       "gpt-5.4-mini",
	"multimedia.video-result-auth-cache-ttl":                  "3h",
	"observability.logs.error-logs-max-files":                 10,
	"observability.usage.redis-usage-queue-retention-seconds": 60,
}

// falseAsks are settings whose default is true, so false is the value that asks for something.
var falseAsks = map[string]bool{
	"routing.session-affinity-subagents": true,
}

// sink gets each setting classify reaches: its path, value and status (a rule's, or unknown),
// the rule's note, and whether the value asks for nothing (quiet).
type sink func(path string, v any, status int, note string, quiet bool)

// unknown is the status of a key no rule names; partly that of a list whose entries hold
// settings that are not brought, which the note names.
const (
	unknown = dead + 1
	partly  = dead + 2
)

// statusWhy is why a setting of status is not brought, when its rule gives no note.
func statusWhy(status int) string {
	switch status {
	case dead:
		return "it did nothing in today's core either"
	case unknown:
		return "not a key today's core or pluxy reads"
	}
	return ""
}

// markRule is the sink of the config's settings outside the provider sections and alias
// channels: a supported setting is brought (unless a reader named it already), any other is
// not brought, with why.
func (x *translator) markRule(path string, v any, status int, note string, quiet bool) {
	why := cmp.Or(note, statusWhy(status))
	switch status {
	case supported:
		x.fill(path, v)
		return
	case later:
		why = "not supported yet: " + why
	case partly:
		x.markAll(Changed, path, v, Row{Why: "not brought from its entries: " + why})
		return
	}
	if quiet && status != dead {
		why += "; it is off, empty or at today's default here, so nothing is lost"
	}
	x.markAll(NotBrought, path, v, Row{Why: why})
}

// classify names, among the settings at path and below, those that are not supported, are
// dropped or are not known, and hands each setting it reaches to sink. inherited is the rule of
// the nearest mapping above that has one.
func (x *translator) classify(path string, v any, inherited *rule, sink sink) {
	r, ok := lookup(path)
	if !ok && inherited != nil {
		r, ok = *inherited, true
	}
	if ok {
		if m, isMap := v.(tree); isMap && len(m) > 0 && rulesBelow(path) && !switchedOff(m) {
			for _, k := range slices.Sorted(maps.Keys(m)) {
				x.classify(path+"."+k, m[k], &r, sink)
			}
			return
		}
		quiet := asksNothing(path, v)
		sink(path, v, r.status, r.note, quiet)
		if quiet {
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
		if path != "" && (len(v) == 0 || switchedOff(v)) {
			sink(path, v, unknown, "", true)
			return
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			p := k
			if path != "" {
				p = path + "." + k
			}
			x.classify(p, v[k], nil, sink)
		}
	case []any:
		// A list is one setting. The rules below its entries say what they hold: when some of
		// that is not brought, the list is brought in part.
		var scalars bool
		var part []string
		for _, c := range v {
			if _, isMap := c.(tree); isMap {
				p := path
				if !strings.HasSuffix(p, "[]") {
					p += "[]"
				}
				x.classify(p, c, nil, func(cp string, _ any, st int, note string, quiet bool) {
					if !quiet && st != supported {
						part = append(part, display(strings.TrimPrefix(trail(cp, p), "."))+" ("+cmp.Or(note, statusWhy(st))+")")
					}
				})
			} else if !isZero(c) {
				x.unknownKeys = append(x.unknownKeys, display(path))
				scalars = true
			}
		}
		switch {
		case scalars:
			sink(path, v, unknown, "", false)
		case len(part) > 0:
			sink(path, v, partly, joinWhy(part), false)
		case rulesBelow(path + "[]"):
			sink(path, v, supported, "", len(v) == 0)
		default:
			sink(path, v, unknown, "", true)
		}
	default:
		quiet := asksNothing(path, v)
		sink(path, v, unknown, "", quiet)
		if !quiet {
			x.unknownKeys = append(x.unknownKeys, display(path))
		}
	}
}

// chains makes a fallback chain (routing.chains) of each name clients use that today's core
// pooled: an openai-compatibility alias over several models, taken in turn, and a name served by
// accounts of more than one provider, whose requests today's core shared among them. pluxy
// serves a name from one provider and one model unless a chain says otherwise, so the chain
// keeps every model and provider, tried in the order the group lists its models and then the
// order core reads the provider sections, rather than in turn.
func (x *translator) chains() error {
	var order []string
	steps := map[string][]route.Step{}
	exts := map[string][]string{}
	items := map[string][]string{}
	addItem := func(name, item string) {
		if !slices.Contains(items[name], item) {
			items[name] = append(items[name], item)
		}
	}
	for _, p := range x.pools {
		if _, ok := steps[p.name]; !ok {
			order = append(order, p.name)
		}
		for _, m := range p.models {
			i := slices.IndexFunc(steps[p.name], func(s route.Step) bool { return s.Model == m && s.Provider == p.ext })
			if i < 0 {
				steps[p.name] = append(steps[p.name], route.Step{Model: m, Provider: p.ext})
				i = len(steps[p.name]) - 1
			}
			if !slices.Contains(steps[p.name][i].Accounts, p.account) {
				steps[p.name][i].Accounts = append(steps[p.name][i].Accounts, p.account)
			}
		}
		addItem(p.name, p.item)
	}
	for _, s := range x.served {
		for _, n := range s.names {
			if !slices.Contains(exts[n], s.ext) {
				exts[n] = append(exts[n], s.ext)
			}
		}
	}
	for _, s := range x.served {
		for _, n := range s.names {
			if len(exts[n]) < 2 {
				continue
			}
			if _, ok := steps[n]; !ok && !slices.Contains(order, n) {
				order = append(order, n)
			}
			addItem(n, s.item)
		}
	}
	if len(order) == 0 {
		return nil
	}
	out := map[string][]route.Step{}
	for _, n := range order {
		st := steps[n]
		for _, e := range exts[n] {
			if !slices.ContainsFunc(st, func(s route.Step) bool { return s.Provider == e }) {
				st = append(st, route.Step{Model: n, Provider: e})
			}
		}
		var cut []string
		if len(st) > route.MaxChainSteps {
			for _, s := range st[route.MaxChainSteps:] {
				cut = append(cut, s.String())
			}
			st = st[:route.MaxChainSteps]
		}
		var text []string
		for _, s := range st {
			text = append(text, s.String())
		}
		why := fmt.Sprintf("today's core shared %q among %s in turn; pluxy tries them in this order as the fallback chain routing.chains.%s", n, strings.Join(text, ", "), n)
		if len(cut) > 0 {
			why += fmt.Sprintf(" (a chain has at most %d steps: %s left out)", route.MaxChainSteps, strings.Join(cut, ", "))
		}
		out[n] = st
		for _, it := range items[n] {
			x.whyFor[it] = append(x.whyFor[it], why)
		}
		x.notes = append(x.notes, "config.yaml: "+why)
	}
	if err := route.ValidateChains(out); err != nil {
		if x.importing {
			// The accounts' models are already cut for the chains (each model listed under its
			// own name), so an import that left the chains out would write accounts no chain
			// serves as the file did. Fail it, naming the pools, as an item in no list does.
			var at []string
			for _, n := range order {
				at = append(at, items[n]...)
			}
			return fmt.Errorf("%s: the models pooled as %s cannot become fallback chains: %v",
				strings.Join(dedupe(at), ", "), quoteAll(order), err)
		}
		// A name that is also a step of another chain; leave the chains out rather than refuse
		// the file.
		x.notes = append(x.notes, "config.yaml: the fallback chains for pooled models are left out: "+err.Error())
		for _, n := range order {
			for _, it := range items[n] {
				x.whyFor[it] = append(x.whyFor[it], "its pooled models could not become a chain: "+err.Error())
			}
		}
		return nil
	}
	r, _ := x.out["routing"].(map[string]any)
	if r == nil {
		r = map[string]any{}
		x.out["routing"] = r
	}
	r["chains"] = out
	return nil
}

// joinWhy joins reasons into one, each once.
func joinWhy(why []string) string {
	var out []string
	for _, w := range why {
		if w != "" && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return strings.Join(out, "; ")
}

func splitWhy(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "; ")
}

// plural is one when n is 1, else many.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// rulesBelow reports whether a setting below path has a rule of its own.
func rulesBelow(path string) bool {
	star := ""
	if rest, ok := strings.CutPrefix(path, "api-keys."); ok {
		if section, tail, _ := strings.Cut(rest, "."); strings.HasSuffix(section, "[]") && tail != "" {
			star = "api-keys.*[]." + tail + "."
		}
	}
	for k := range rules {
		if strings.HasPrefix(k, path+".") || star != "" && strings.HasPrefix(k, star) {
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
			on, _ := boolean_(v, k)
			return !on
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
	if value(v) == false && falseAsks[path] {
		return false
	}
	if d, ok := defaults[path]; ok && d == value(v) {
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

// quoteAll writes names as quoted strings separated by commas.
func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}

func isZero(v any) bool {
	switch v := value(v).(type) {
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

// str reads the text setting at path (see scalar).
func str(t tree, path string) (string, error) {
	v, _ := get(t, path)
	return scalar(v, path)
}

func integer(t tree, path string) (int, error) {
	v, ok := get(t, path)
	if !ok || v == nil {
		return 0, nil
	}
	switch v := value(v).(type) {
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

// scalar reads a text setting as today's core's typed decoder does: a scalar is its text as
// written, whatever YAML reads it as (api-key: 0123 is "0123", true is "true"), and absent or
// null is empty.
func scalar(v any, path string) (string, error) {
	switch v := v.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case literal:
		return v.text, nil
	}
	return "", fmt.Errorf("%s must be text", path)
}

// headers reads a headers mapping as today's core does (upheader.Import): each value is a
// scalar, read as its text (see scalar).
func headers(v any, path string) (map[string]string, []upheader.Left, error) {
	if v == nil {
		return nil, nil, nil
	}
	m, ok := v.(tree)
	if !ok {
		return nil, nil, fmt.Errorf("%s must be a mapping", path)
	}
	text := map[string]string{}
	for k, val := range m {
		s, err := scalar(val, path+"."+k)
		if err != nil {
			return nil, nil, err
		}
		text[k] = s
	}
	h, left, err := upheader.Import(text)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return h, left, nil
}

// HeaderText returns a header value of a provider section of a config.yaml as the header core
// sends: its text as written, without the spaces around it (upheader.ImportValue). ok is false for
// a value core refuses.
func HeaderText(n *yaml.Node) (text string, ok bool) {
	var d node
	if n.Decode(&d) != nil {
		return "", false
	}
	s, err := scalar(d.v, "")
	return upheader.ImportValue(s), err == nil
}

// boolean_ reads a true or false setting; absent is false. As today's core's YAML decoder
// (gopkg.in/yaml.v3) reads a setting it decodes into a true-or-false field, the YAML 1.1 words
// y, yes, on, n, no and off (in lower, title or upper case) are true or false too.
func boolean_(v any, path string) (bool, error) {
	switch v := value(v).(type) {
	case nil:
		return false, nil
	case bool:
		return v, nil
	case string:
		switch v {
		case "y", "Y", "yes", "Yes", "YES", "on", "On", "ON":
			return true, nil
		case "n", "N", "no", "No", "NO", "off", "Off", "OFF":
			return false, nil
		}
	}
	return false, fmt.Errorf("%s must be true or false", path)
}

// flag reads the true or false setting at path.
func flag(t tree, path string) (bool, error) {
	v, _ := get(t, path)
	return boolean_(v, path)
}

// ExampleClientKeys are the client keys of today's example config (testdata/config.example.yaml).
// They are public, so a config that still holds one has not set up access.
var ExampleClientKeys = []string{"your-api-key-1", "your-api-key-2", "your-api-key-3"}

// keyless reports a key with no api-key in the provider section at path.
func (x *translator) keyless(path string) {
	x.laterKeys = append(x.laterKeys, display(path)+" keys without an api-key (an upstream that needs no key)")
}
