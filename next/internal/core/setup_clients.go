package core

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/stdotwork/pluxy/next/management"
)

const (
	stateNotConfigured = management.ClientStateNotConfigured
	stateConfigured    = management.ClientStateConfigured
	stateNeedsUpdate   = management.ClientStateNeedsUpdate
	stateRefused       = management.ClientStateRefused
	stateUnchecked     = management.ClientStateUnchecked
)

var errNoFile = errors.New("no file")

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

func blank(b []byte) bool { return len(bytes.TrimLeft(b, " \t\r\n")) == 0 }

const utf8BOM = "\xEF\xBB\xBF"

func jsonObject(b []byte) (map[string]any, bool) {
	var m map[string]any
	if bytes.HasPrefix(b, []byte(utf8BOM)) || json.Unmarshal(b, &m) != nil || m == nil {
		return nil, false
	}
	return m, true
}

func objectOrAbsent(m map[string]any, k string) (map[string]any, bool) {
	v, ok := m[k]
	if !ok {
		return nil, true
	}
	o, ok := v.(map[string]any)
	return o, ok
}

func stringOrAbsent(m map[string]any, k string) bool {
	v, ok := m[k]
	if !ok {
		return true
	}
	_, ok = v.(string)
	return ok
}

const claudeCodeSchema = "https://json.schemastore.org/claude-code-settings.json"

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
		atCore(base, coreURL):
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

func atCore(address, coreURL string) bool {
	if coreURL == "" {
		return false
	}
	if address == coreURL {
		return true
	}
	a, errA := url.Parse(address)
	c, errC := url.Parse(coreURL)
	if errA != nil || errC != nil || a.Scheme != c.Scheme || a.Port() == "" || a.Port() != c.Port() ||
		a.Path != c.Path || a.Opaque != "" || a.User != nil || a.RawQuery != "" || a.Fragment != "" {
		return false
	}
	host := a.Hostname()
	if strings.EqualFold(host, c.Hostname()) {
		return true
	}
	listener, err := netip.ParseAddr(c.Hostname())
	if err != nil || !listener.IsUnspecified() {
		return false
	}
	both := wildcardDualStack()
	if strings.EqualFold(host, "localhost") {
		return both
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if ip.Is4() != listener.Is4() && !both {
		return false
	}
	return ip.IsLoopback() || machineHasAddr(ip)
}

func atCoreV1(address, coreURL string) bool {
	base, ok := strings.CutSuffix(address, "/v1")
	return ok && atCore(base, coreURL)
}

// Go opens a "tcp" listener on 0.0.0.0, :: or no host as one IPv6 socket that takes IPv4 as well
// when the system allows it (it turns IPV6_V6ONLY off, whatever the system's default), and
// otherwise as a socket of the address's own family. A listener on 0.0.0.0 opened the same way
// shows which: the system names it by an IPv6 address only in the first case.
var wildcardDualStack = sync.OnceValue(func() bool {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return false
	}
	defer ln.Close()
	a, ok := ln.Addr().(*net.TCPAddr)
	return ok && a.IP.To4() == nil
})

var machineHasAddr = func(ip netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if own, ok := netip.AddrFromSlice(n.IP); ok && own.Unmap() == ip {
				return true
			}
		}
	}
	return false
}

func codexBaseURL(address string) string {
	a := strings.TrimRight(address, "/")
	if strings.HasSuffix(a, "/v1") {
		return a
	}
	return a + "/v1"
}

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
	case parsed.bearerToken != "" && parsed.modelProvider == "pluxy" && atCoreV1(parsed.baseURL, coreURL):
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

var codexTables = []string{"model_providers", "model_providers.pluxy", "model_providers.pluxy.capabilities", "agents"}

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

const openCodeSchema = "https://opencode.ai/config.json"

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
		atCoreV1(baseURL, coreURL) {
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

func hasDuplicateKey(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	var walk func() bool
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

const (
	piAdapterFolder  = "pluxy-cliproxyapi-provider"
	piAdapterMarker  = ".pluxy-adapter.json"
	piAdapterPackage = "pluxy/pi-setup"
)

func piBaseURL(address string) string {
	a := strings.TrimRight(address, "/")
	return strings.TrimSuffix(a, "/v1")
}

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
	if piAdapterReady(agent) && hasURL && atCore(piBaseURL(fileURL), coreURL) &&
		apiKey != "" && defProvider == providerID && defModel != "" {
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

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

func ompBaseURL(address string) string { return codexBaseURL(address) }

func ompFile(dir string, legacy string, names ...string) (name string, unmigrated bool) {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return n, false
		}
	}
	_, err := os.Stat(filepath.Join(dir, legacy))
	return names[0], err == nil
}

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

func ompMapping(m map[string]any, k string) (map[string]any, bool) {
	if m[k] == nil {
		return nil, true
	}
	o, ok := m[k].(map[string]any)
	return o, ok
}

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
	baseOK := hasBase && atCoreV1(base, coreURL)
	if baseOK && apiKey != "" && (api == "openai-responses" || api == "openai-completions") &&
		discType == "openai-models-list" && model != "" {
		return display, stateConfigured
	}
	return display, stateNeedsUpdate
}

type ClientSnippet struct {
	Tool  string              `json:"tool"`
	Name  string              `json:"name"`
	Files []ClientSnippetFile `json:"files"`
	Note  string              `json:"note,omitempty"`
}

type ClientSnippetFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

var ClientSnippetTools = []string{"claude-code", "codex", "opencode", "pi", "omp"}

var clientSnippetNames = map[string]string{
	"claude-code": "claude-code", "claude": "claude-code", "pluxy/claude-code": "claude-code",
	"codex": "codex", "codex-cli": "codex", "pluxy/codex-setup": "codex",
	"opencode": "opencode", "pluxy/opencode": "opencode",
	"pi": "pi", "pluxy/pi-setup": "pi",
	"omp": "omp", "oh-my-pi": "omp", "pluxy/omp-setup": "omp",
}

func prettyJSON(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

const ModelPlaceholder = "<model-id>"

func ClientSnippetFor(tool, baseURL, key, model string) (ClientSnippet, bool) {
	if model == "" {
		model = ModelPlaceholder
	}
	return clientSnippet(tool, baseURL, key, model)
}

func clientSnippetID(tool string) (string, bool) {
	id, ok := clientSnippetNames[strings.ToLower(strings.TrimSpace(tool))]
	return id, ok
}

func clientSnippet(tool, baseURL, key, model string) (ClientSnippet, bool) {
	id, ok := clientSnippetID(tool)
	if !ok {
		return ClientSnippet{}, false
	}
	base := strings.TrimRight(baseURL, "/")
	spec, _ := clientSpecByID(id)
	s := ClientSnippet{Tool: id, Name: spec.name}
	switch id {
	case "claude-code":
		// Claude Code asks for its own model names (claude-opus-5-5 and the like) unless told
		// otherwise, and an account of another service serves none of them: `model` is the one it
		// starts with, and the three ANTHROPIC_DEFAULT_*_MODEL are what its opus, sonnet and
		// haiku choices, and the small model it uses in the background, resolve to.
		env := map[string]string{
			"ANTHROPIC_BASE_URL":                         base,
			"ANTHROPIC_AUTH_TOKEN":                       key,
			"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
			"CLAUDE_CODE_AUTO_MODE_SERVER":               "0",
		}
		doc := map[string]any{"env": env}
		if model != "" {
			doc["model"] = model
			env["ANTHROPIC_DEFAULT_OPUS_MODEL"] = model
			env["ANTHROPIC_DEFAULT_SONNET_MODEL"] = model
			env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = model
		}
		s.Files = []ClientSnippetFile{{"~/.claude/settings.json", prettyJSON(doc)}}
	case "codex":
		head := "model_provider = \"pluxy\"\n"
		if model != "" {
			head += fmt.Sprintf("model = %q\n", model)
		}
		s.Files = []ClientSnippetFile{{"~/.codex/config.toml", fmt.Sprintf(`%s
[model_providers.pluxy]
name = "pluxy"
base_url = %q
wire_api = "responses"
experimental_bearer_token = %q`, head, codexBaseURL(base), key)}}
		s.Note = codexTOMLNote
	case "opencode":
		m := cmp.Or(model, ModelPlaceholder)
		s.Files = []ClientSnippetFile{{"~/.config/opencode/opencode.jsonc", prettyJSON(map[string]any{
			"$schema": openCodeSchema,
			"provider": map[string]any{
				"pluxy": map[string]any{
					"npm":     "@ai-sdk/openai-compatible",
					"name":    "pluxy",
					"options": map[string]string{"baseURL": base + "/v1", "apiKey": key},
					"models":  map[string]any{m: map[string]string{"name": m}},
				},
			},
			"model": "pluxy/" + m,
		})}}
	case "pi":
		m := cmp.Or(model, ModelPlaceholder)
		s.Files = []ClientSnippetFile{
			{"~/.pi/agent/cliproxyapi.json", prettyJSON(map[string]string{"baseUrl": piBaseURL(base), "apiKey": key})},
			{"~/.pi/agent/settings.json", prettyJSON(map[string]any{
				"packages":        []string{"./" + piAdapterFolder},
				"defaultProvider": "cliproxyapi",
				"defaultModel":    m,
			})},
		}
		s.Note = "Pi also needs pluxy's adapter in ~/.pi/agent/" + piAdapterFolder + ", which the desktop app installs."
	case "omp":
		m := cmp.Or(model, ModelPlaceholder)
		s.Files = []ClientSnippetFile{
			{"~/.omp/agent/models.yml", fmt.Sprintf(`providers:
  pluxy:
    baseUrl: %q
    apiKey: %q
    api: openai-responses
    discovery:
      type: openai-models-list`, ompBaseURL(base), key)},
			{"~/.omp/agent/config.yml", fmt.Sprintf(`modelRoles:
  default: %q`, "pluxy/"+m)},
		}
	}
	return s, true
}
