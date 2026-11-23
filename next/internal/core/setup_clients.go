package core

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/management"
)

// The client tools' config files, read the way the desktop app's writers for them read them
// (next/app-tools/<tool>/src): a file a writer would refuse to change is refused, and a file
// it takes is configured when it points the tool at this core, not configured when it holds
// none of what the writer adds, and needs an update otherwise. coreURL is core's address as a
// program on this machine reaches it (server.LocalURL), compared as text as the writers do;
// empty when core's address is not known, which, as in the writers, any address matches.

const (
	stateNotConfigured = management.ClientStateNotConfigured
	stateConfigured    = management.ClientStateConfigured
	stateNeedsUpdate   = management.ClientStateNeedsUpdate
	stateRefused       = management.ClientStateRefused
)

var errNoFile = errors.New("no file")

// readClientFile reads a client tool's config file: errNoFile when there is none, and anything
// but a regular file refused without reading it, since reading a FIFO waits for a writer and
// would hold the scan and core's stop.
func readClientFile(path string) ([]byte, error) {
	f, err := openClientFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, errNoFile
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxClientConfig+1))
	if err == nil && len(b) > maxClientConfig {
		err = fmt.Errorf("%s is larger than %d bytes", path, maxClientConfig)
	}
	return b, err
}

// blank reports whether b is empty or only white space, which the writers read as no settings.
func blank(b []byte) bool { return len(bytes.TrimLeft(b, " \t\r\n")) == 0 }

const utf8BOM = "\xEF\xBB\xBF"

// jsonObject reads b as one strict JSON object, as serde_json and JSON.parse read it: a
// byte-order mark, anything but an object, or text after it is refused.
func jsonObject(b []byte) (map[string]any, bool) {
	var m map[string]any
	if bytes.HasPrefix(b, []byte(utf8BOM)) || json.Unmarshal(b, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

// objectOrAbsent is m[k] as an object: absent is (nil, true); anything else but an object is
// (nil, false).
func objectOrAbsent(m map[string]any, k string) (map[string]any, bool) {
	v, ok := m[k]
	if !ok {
		return nil, true
	}
	o, ok := v.(map[string]any)
	return o, ok
}

// stringOrAbsent reports whether m[k] is a string or absent.
func stringOrAbsent(m map[string]any, k string) bool {
	v, ok := m[k]
	if !ok {
		return true
	}
	_, ok = v.(string)
	return ok
}

// claudeCodeSchema is the schema Claude Code's settings may name in $schema.
const claudeCodeSchema = "https://json.schemastore.org/claude-code-settings.json"

// inspectClaudeCode reads ~/.claude/settings.json as settings.rs (parse and read_back) does.
func inspectClaudeCode(home, coreURL string) (string, string) {
	const display = "~/.claude/settings.json"
	b, err := readClientFile(filepath.Join(home, ".claude", "settings.json"))
	switch {
	case errors.Is(err, errNoFile):
		return display, stateNotConfigured
	case err != nil:
		return display, stateRefused
	case blank(b):
		return display, stateNotConfigured
	}
	root, ok := jsonObject(b)
	if !ok {
		return display, stateRefused
	}
	if v, ok := root["$schema"]; ok && v != claudeCodeSchema {
		return display, stateRefused
	}
	env, ok := objectOrAbsent(root, "env")
	if !ok {
		return display, stateRefused
	}
	for _, v := range env {
		if _, ok := v.(string); !ok {
			return display, stateRefused
		}
	}
	str := func(k string) string { s, _ := env[k].(string); return s }
	base, ok := env["ANTHROPIC_BASE_URL"].(string)
	switch {
	case !ok:
		return display, stateNotConfigured
	case str("ANTHROPIC_AUTH_TOKEN") != "" &&
		str("CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY") == "1" &&
		str("CLAUDE_CODE_AUTO_MODE_SERVER") == "0" &&
		(coreURL == "" || base == coreURL):
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

// codexBaseURL is the Codex provider's base URL for the core at address: the address with /v1
// once (base_url_of in codex/src/config.rs).
func codexBaseURL(address string) string {
	a := strings.TrimRight(address, "/")
	if strings.HasSuffix(a, "/v1") {
		return a
	}
	return a + "/v1"
}

// inspectCodex reads ~/.codex/config.toml as config.rs (parse and read_back) does, with one
// difference: config.rs also wants model_catalog_json, the model catalog file the desktop app
// writes beside the config. What pluxy client show prints carries no catalog, so a config
// pasted from it counts as configured without one.
func inspectCodex(home, coreURL string) (string, string) {
	const display = "~/.codex/config.toml"
	b, err := readClientFile(filepath.Join(home, ".codex", "config.toml"))
	switch {
	case errors.Is(err, errNoFile):
		return display, stateNotConfigured
	case err != nil:
		return display, stateRefused
	}
	parsed := parseCodexConfigTOML(b)
	switch {
	case parsed.refused:
		return display, stateRefused
	case !parsed.hasBaseURL:
		return display, stateNotConfigured
	case parsed.bearerToken != "" && parsed.modelProvider == "pluxy" &&
		(coreURL == "" || parsed.baseURL == codexBaseURL(coreURL)):
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

type codexTOMLFields struct {
	refused       bool
	modelProvider string
	hasBaseURL    bool
	baseURL       string
	bearerToken   string
}

// codexTables are the tables config.rs refuses a file for holding anything else at.
var codexTables = []string{"model_providers", "model_providers.pluxy", "model_providers.pluxy.capabilities", "agents"}

// parseCodexConfigTOML reads, line by line, only the keys inspectCodex needs and the refusals
// config.rs makes that a line shows: text that is not UTF-8, one of codexTables written as an
// array of tables or a value, and model_providers.pluxy.wire_api other than "responses". It is
// not a TOML parser: a file TOML itself would refuse can still read as not refused here.
func parseCodexConfigTOML(b []byte) codexTOMLFields {
	var out codexTOMLFields
	if !utf8.Valid(b) {
		out.refused = true
		return out
	}
	section := ""
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(stripTOMLComment(raw))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			section = normalizeTOMLPath(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			if slices.Contains(codexTables, section) {
				out.refused = true
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = normalizeTOMLPath(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		eq := indexUnquoted(line, '=')
		if eq < 0 {
			continue
		}
		k := normalizeTOMLPath(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if section != "" && k != "" {
			k = section + "." + k
		}
		if slices.Contains(codexTables, k) {
			out.refused = true
			continue
		}
		s, isString := parseTOMLString(val)
		switch k {
		case "model_provider":
			if isString {
				out.modelProvider = s
			}
		case "model_providers.pluxy.base_url":
			if isString {
				out.hasBaseURL = true
				out.baseURL = s
			}
		case "model_providers.pluxy.experimental_bearer_token":
			if isString {
				out.bearerToken = s
			}
		case "model_providers.pluxy.wire_api":
			if !isString || s != "responses" {
				out.refused = true
			}
		}
	}
	return out
}

func stripTOMLComment(line string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '#' {
			return line[:i]
		}
	}
	return line
}

func indexUnquoted(s string, target byte) int {
	var quote byte
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if quote == '"' && c == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == target {
			return i
		}
	}
	return -1
}

func normalizeTOMLPath(s string) string {
	var parts []string
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '.') {
			i++
		}
		if i >= len(s) {
			break
		}
		if s[i] == '"' || s[i] == '\'' {
			q := s[i]
			i++
			start := i
			for i < len(s) && s[i] != q {
				if q == '"' && s[i] == '\\' && i+1 < len(s) {
					i++
				}
				i++
			}
			parts = append(parts, s[start:i])
			if i < len(s) {
				i++
			}
			continue
		}
		start := i
		for i < len(s) && s[i] != '.' && s[i] != ' ' && s[i] != '\t' {
			i++
		}
		parts = append(parts, s[start:i])
	}
	return strings.Join(parts, ".")
}

func parseTOMLString(val string) (string, bool) {
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		var s string
		if json.Unmarshal([]byte(val), &s) == nil {
			return s, true
		}
		return val[1 : len(val)-1], true
	}
	if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
		return val[1 : len(val)-1], true
	}
	return "", false
}

// openCodeSchema is the schema OpenCode's config may name in $schema.
const openCodeSchema = "https://opencode.ai/config.json"

// inspectOpenCode reads ~/.config/opencode/opencode.jsonc, or opencode.json when only that one
// exists, as opencode's config.rs (parse and read_back) and main.rs (config_in) do. A file
// with providers.pluxy, OpenCode 2's own form of the provider, which config.rs's apply refuses
// to write over, is refused.
func inspectOpenCode(home, coreURL string) (string, string) {
	dir := filepath.Join(home, ".config", "opencode")
	name := "opencode.jsonc"
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		if _, err := os.Stat(filepath.Join(dir, "opencode.json")); err == nil {
			name = "opencode.json"
		}
	}
	display := "~/.config/opencode/" + name
	b, err := readClientFile(filepath.Join(dir, name))
	switch {
	case errors.Is(err, errNoFile):
		return display, stateNotConfigured
	case err != nil || bytes.HasPrefix(b, []byte(utf8BOM)) || !utf8.Valid(b):
		return display, stateRefused
	case blank(b):
		return display, stateNotConfigured
	}
	clean, ok := cleanJSONC(b)
	if !ok {
		return display, stateRefused
	}
	root, ok := jsonObject(clean)
	if !ok || hasDuplicateKey(clean) {
		return display, stateRefused
	}
	if v, ok := root["$schema"]; ok && v != openCodeSchema {
		return display, stateRefused
	}
	provider, ok := objectOrAbsent(root, "provider")
	if !ok {
		return display, stateRefused
	}
	p, ok := objectOrAbsent(provider, "pluxy")
	if !ok {
		return display, stateRefused
	}
	options, ok := objectOrAbsent(p, "options")
	if !ok {
		return display, stateRefused
	}
	models, ok := objectOrAbsent(p, "models")
	if !ok {
		return display, stateRefused
	}
	for _, m := range models {
		if _, ok := m.(map[string]any); !ok {
			return display, stateRefused
		}
	}
	var ours string
	switch m := root["model"].(type) {
	case nil:
		if _, set := root["model"]; set {
			return display, stateRefused
		}
	case string:
		if rest, ok := strings.CutPrefix(m, "pluxy/"); ok {
			ours = rest
		}
	case map[string]any:
		pid, okP := m["providerID"].(string)
		model, okM := m["model"].(string)
		if !okP || !okM {
			return display, stateRefused
		}
		if pid == "pluxy" {
			ours = model
		}
	default:
		return display, stateRefused
	}
	if v2, _ := root["providers"].(map[string]any); v2 != nil {
		if _, ok := v2["pluxy"]; ok {
			return display, stateRefused
		}
	}
	if p == nil {
		return display, stateNotConfigured
	}
	apiKey, _ := options["apiKey"].(string)
	baseURL, hasURL := options["baseURL"].(string)
	_, listed := models[ours]
	if apiKey != "" && p["npm"] == "@ai-sdk/openai-compatible" && ours != "" && listed && hasURL &&
		(coreURL == "" || baseURL == strings.TrimRight(coreURL, "/")+"/v1") {
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

// hasDuplicateKey reports whether the JSON in b names a key twice in one object, at any depth,
// which opencode's config.rs refuses.
func hasDuplicateKey(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func() bool // reads one value; true when it holds a repeated key
	walk = func() bool {
		t, err := dec.Token()
		if err != nil {
			return false
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return false
				}
				name, _ := k.(string)
				if seen[name] || walk() {
					return true
				}
				seen[name] = true
			}
			_, _ = dec.Token()
		case json.Delim('['):
			for dec.More() {
				if walk() {
					return true
				}
			}
			_, _ = dec.Token()
		}
		return false
	}
	return walk()
}

// cleanJSONC strips // and /* */ comments and trailing commas from JSONC input. An unclosed
// block comment is false, not a comment running to the end of the file: a reader that parses
// the whole file refuses one, so the file is not one to call configured.
func cleanJSONC(in []byte) ([]byte, bool) {
	var noComments bytes.Buffer
	noComments.Grow(len(in))
	inString := false
	escaped := false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inString {
			noComments.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			noComments.WriteByte(c)
			continue
		}
		if c == '/' && i+1 < len(in) {
			if in[i+1] == '/' {
				i += 2
				for i < len(in) && in[i] != '\n' {
					i++
				}
				if i < len(in) {
					noComments.WriteByte('\n')
				}
				continue
			}
			if in[i+1] == '*' {
				end := bytes.Index(in[i+2:], []byte("*/"))
				if end < 0 {
					return nil, false
				}
				i += end + 3
				continue
			}
		}
		noComments.WriteByte(c)
	}

	b := noComments.Bytes()
	var out bytes.Buffer
	out.Grow(len(b))
	inString = false
	escaped = false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			out.WriteByte(c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes(), true
}

// The Pi adapter: its folder in Pi's agent folder, the marker file that makes the folder
// pluxy's, and the package the marker names (pi-setup/src/pi.rs and adapter.rs).
const (
	piAdapterFolder  = "pluxy-cliproxyapi-provider"
	piAdapterMarker  = ".pluxy-adapter.json"
	piAdapterPackage = "pluxy/pi-setup"
)

// piBaseURL is the core's address as the Pi adapter takes it: without a final / or /v1
// (base_url_of in pi.rs).
func piBaseURL(address string) string {
	a := strings.TrimRight(address, "/")
	return strings.TrimSuffix(a, "/v1")
}

// piObject reads one of Pi's files as pi.rs's object does: no file is an empty object; a
// byte-order mark, anything but one JSON object (an empty file too) is refused.
func piObject(path string) (map[string]any, bool) {
	b, err := readClientFile(path)
	if errors.Is(err, errNoFile) {
		return map[string]any{}, true
	}
	if err != nil {
		return nil, false
	}
	return jsonObject(b)
}

// piSource is a packages entry's source: the string, or an object's source.
func piSource(entry any) (string, bool) {
	switch e := entry.(type) {
	case string:
		return e, true
	case map[string]any:
		s, ok := e["source"].(string)
		return s, ok
	}
	return "", false
}

// inspectPi reads ~/.pi/agent/settings.json and cliproxyapi.json, and the adapter's marker,
// as pi.rs (read_back) and adapter.rs (state) do.
func inspectPi(home, coreURL string) (string, string) {
	const display = "~/.pi/agent/settings.json"
	agent := filepath.Join(home, ".pi", "agent")
	cfg, ok := piObject(filepath.Join(agent, "cliproxyapi.json"))
	if !ok || !stringOrAbsent(cfg, "baseUrl") || !stringOrAbsent(cfg, "apiKey") || !stringOrAbsent(cfg, "providerId") {
		return display, stateRefused
	}
	st, ok := piObject(filepath.Join(agent, "settings.json"))
	if !ok || !stringOrAbsent(st, "defaultProvider") || !stringOrAbsent(st, "defaultModel") {
		return display, stateRefused
	}
	registered := false
	if v, set := st["packages"]; set {
		list, ok := v.([]any)
		if !ok {
			return display, stateRefused
		}
		for _, e := range list {
			src, ok := piSource(e)
			if !ok {
				return display, stateRefused
			}
			src = strings.TrimRight(strings.TrimSpace(src), "/")
			if strings.TrimPrefix(src, "./") == piAdapterFolder {
				registered = true
			}
		}
	}
	if !registered {
		return display, stateNotConfigured
	}
	fileURL, hasURL := cfg["baseUrl"].(string)
	apiKey, _ := cfg["apiKey"].(string)
	providerID, _ := cfg["providerId"].(string)
	providerID = cmp.Or(strings.TrimSpace(providerID), "cliproxyapi")
	defProvider, _ := st["defaultProvider"].(string)
	defModel, _ := st["defaultModel"].(string)
	if piAdapterReady(agent) && hasURL && (coreURL == "" || piBaseURL(fileURL) == piBaseURL(coreURL)) &&
		apiKey != "" && defProvider == providerID && defModel != "" {
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

// piAdapterReady reports whether the adapter's folder in agent is pluxy's: a folder (not a
// link) whose marker names pluxy's package.
func piAdapterReady(agent string) bool {
	dir := filepath.Join(agent, piAdapterFolder)
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return false
	}
	b, err := readClientFile(filepath.Join(dir, piAdapterMarker))
	if err != nil {
		return false
	}
	var marker struct {
		Package any `json:"package"`
	}
	return json.Unmarshal(b, &marker) == nil && marker.Package == piAdapterPackage
}

// ompBaseURL is the pluxy provider's baseUrl for the core at address: with /v1 once
// (base_url_of in omp.rs).
func ompBaseURL(address string) string { return codexBaseURL(address) }

// ompFile picks the first of names that exists in dir, else the first, as omp-setup's main.rs
// (pick) does; legacy is the JSON file Oh My Pi moves to YAML when neither exists.
func ompFile(dir string, legacy string, names ...string) (name string, unmigrated bool) {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return n, false
		}
	}
	_, err := os.Stat(filepath.Join(dir, legacy))
	return names[0], err == nil
}

// ompYAML reads one of Oh My Pi's YAML files as the app's YAML reader does: no file, no
// document or a null one is an empty mapping; more than one document, a top level that is not
// a mapping, or what yaml.v3 refuses (a repeated key among it) is refused. YAML tags, which
// the app's reader also refuses, are not looked for.
func ompYAML(path string) (map[string]any, bool) {
	b, err := readClientFile(path)
	if errors.Is(err, errNoFile) {
		return map[string]any{}, true
	}
	if err != nil || !utf8.Valid(b) {
		return nil, false
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc any
	if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
		return map[string]any{}, true
	} else if err != nil {
		return nil, false
	}
	var more any
	if err := dec.Decode(&more); !errors.Is(err, io.EOF) {
		return nil, false
	}
	switch d := doc.(type) {
	case nil:
		return map[string]any{}, true
	case map[string]any:
		return d, true
	}
	return nil, false
}

// ompMapping is m[k] as a mapping: absent or null is (nil, true); anything else but a mapping
// is (nil, false).
func ompMapping(m map[string]any, k string) (map[string]any, bool) {
	if m[k] == nil {
		return nil, true
	}
	o, ok := m[k].(map[string]any)
	return o, ok
}

// inspectOMP reads ~/.omp/agent/models.yml (or models.yaml) and config.yml (or config.yaml) as
// omp-setup's omp.rs (read_back) and main.rs (config_of) do.
func inspectOMP(home, coreURL string) (string, string) {
	agent := filepath.Join(home, ".omp", "agent")
	modelsName, modelsOld := ompFile(agent, "models.json", "models.yml", "models.yaml")
	configName, configOld := ompFile(agent, "settings.json", "config.yml", "config.yaml")
	display := "~/.omp/agent/" + modelsName
	if modelsOld || configOld {
		return display, stateRefused
	}
	models, ok := ompYAML(filepath.Join(agent, modelsName))
	if !ok {
		return display, stateRefused
	}
	config, ok := ompYAML(filepath.Join(agent, configName))
	if !ok {
		return display, stateRefused
	}
	providers, ok := ompMapping(models, "providers")
	if !ok {
		return display, stateRefused
	}
	p, ok := ompMapping(providers, "pluxy")
	if !ok {
		return display, stateRefused
	}
	discovery, ok := ompMapping(p, "discovery")
	if !ok {
		return display, stateRefused
	}
	roles, ok := ompMapping(config, "modelRoles")
	if !ok {
		return display, stateRefused
	}
	var model string
	switch d := roles["default"].(type) {
	case nil:
	case string:
		if m, ok := strings.CutPrefix(d, "pluxy/"); ok {
			model = m
		}
	case []any:
		for _, v := range d {
			if _, ok := v.(string); !ok {
				return display, stateRefused
			}
		}
	default:
		return display, stateRefused
	}
	if p == nil && model == "" {
		return display, stateNotConfigured
	}
	base, hasBase := p["baseUrl"].(string)
	apiKey, _ := p["apiKey"].(string)
	api, _ := p["api"].(string)
	discType, _ := discovery["type"].(string)
	baseOK := hasBase && (coreURL == "" && base != "" || coreURL != "" && base == ompBaseURL(coreURL))
	if baseOK && apiKey != "" && (api == "openai-responses" || api == "openai-completions") &&
		discType == "openai-models-list" && model != "" {
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

// ClientSnippet is what to paste into a client tool's config files to point it at a core.
type ClientSnippet struct {
	Tool  string              `json:"tool"`
	Name  string              `json:"name"`
	Files []ClientSnippetFile `json:"files"`
	// Note says what the files need besides themselves; empty when nothing.
	Note string `json:"note,omitempty"`
}

// ClientSnippetFile is one file of a ClientSnippet: where it goes (from the home folder, ~) and
// the settings to put in it.
type ClientSnippetFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ClientSnippetTools are the tools ClientSnippetFor knows, by their SetupClient ID.
var ClientSnippetTools = []string{"claude-code", "codex", "opencode", "pi", "omp"}

// clientSnippetNames maps what a person may call a tool (its ID, its setup package's ID, a
// longer name) to its SetupClient ID.
var clientSnippetNames = map[string]string{
	"claude-code": "claude-code", "claude": "claude-code", "pluxy/claude-code": "claude-code",
	"codex": "codex", "codex-cli": "codex", "pluxy/codex-setup": "codex",
	"opencode": "opencode", "pluxy/opencode": "opencode",
	"pi": "pi", "pluxy/pi-setup": "pi",
	"omp": "omp", "oh-my-pi": "omp", "pluxy/omp-setup": "omp",
}

// prettyJSON is v as indented JSON, with <, > and & as they are (a key placeholder has them).
func prettyJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

// ClientSnippetFor is the snippet that points tool at the core at baseURL (its address, with
// no /v1) with the client key key, false for a tool it does not know. Each file holds what the
// tool's writer in the desktop app writes for inspect to read it as configured.
func ClientSnippetFor(tool, baseURL, key string) (ClientSnippet, bool) {
	id, ok := clientSnippetNames[strings.ToLower(strings.TrimSpace(tool))]
	if !ok {
		return ClientSnippet{}, false
	}
	base := strings.TrimRight(baseURL, "/")
	spec, _ := clientSpecByID(id)
	s := ClientSnippet{Tool: id, Name: spec.name}
	switch id {
	case "claude-code":
		s.Files = []ClientSnippetFile{{"~/.claude/settings.json", prettyJSON(map[string]any{
			"env": map[string]string{
				"ANTHROPIC_BASE_URL":                         base,
				"ANTHROPIC_AUTH_TOKEN":                       key,
				"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
				"CLAUDE_CODE_AUTO_MODE_SERVER":               "0",
			},
		})}}
	case "codex":
		s.Files = []ClientSnippetFile{{"~/.codex/config.toml", fmt.Sprintf(`model_provider = "pluxy"

[model_providers.pluxy]
name = "pluxy"
base_url = %q
wire_api = "responses"
experimental_bearer_token = %q`, codexBaseURL(base), key)}}
	case "opencode":
		s.Files = []ClientSnippetFile{{"~/.config/opencode/opencode.jsonc", prettyJSON(map[string]any{
			"$schema": openCodeSchema,
			"provider": map[string]any{
				"pluxy": map[string]any{
					"npm":     "@ai-sdk/openai-compatible",
					"name":    "pluxy",
					"options": map[string]string{"baseURL": base + "/v1", "apiKey": key},
					"models":  map[string]any{"<model-id>": map[string]string{"name": "<model-id>"}},
				},
			},
			"model": "pluxy/<model-id>",
		})}}
	case "pi":
		s.Files = []ClientSnippetFile{
			{"~/.pi/agent/cliproxyapi.json", prettyJSON(map[string]string{"baseUrl": piBaseURL(base), "apiKey": key})},
			{"~/.pi/agent/settings.json", prettyJSON(map[string]any{
				"packages":        []string{"./" + piAdapterFolder},
				"defaultProvider": "cliproxyapi",
				"defaultModel":    "<model-id>",
			})},
		}
		s.Note = "Pi also needs pluxy's adapter in ~/.pi/agent/" + piAdapterFolder + ", which the desktop app installs."
	case "omp":
		s.Files = []ClientSnippetFile{
			{"~/.omp/agent/models.yml", fmt.Sprintf(`providers:
  pluxy:
    baseUrl: %q
    apiKey: %q
    api: openai-responses
    discovery:
      type: openai-models-list`, ompBaseURL(base), key)},
			{"~/.omp/agent/config.yml", `modelRoles:
  default: "pluxy/<model-id>"`},
		}
	}
	return s, true
}
