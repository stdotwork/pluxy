package discovery

import (
	"fmt"
	"strings"
)

const (
	maxTXTRecordBytes = 255 // RFC 6763 §6.1 single character-string limit
	maxTXTBytes       = 400 // draft-su-ai-gateway-discovery-00 TXT budget

	txtSchemaVersion = "1"
	defaultMountPath = "/"
)

// TXT keys defined by draft-su-ai-gateway-discovery-00.
// Every key is at most nine characters long (RFC 6763 §6.4).
const (
	txtKeyVersion   = "txtvers"
	txtKeyPath      = "path"
	txtKeyTLS       = "tls"
	txtKeyAuth      = "auth"
	txtKeyProtocols = "protocols"
	txtKeyAuthType  = "auth_type"
	txtKeyProduct   = "product"
	txtKeyID        = "id"
	txtKeyRole      = "role"
)

// Legacy TXT keys advertised by CPA releases before the draft schema.
// Browsers still accept them so older gateways remain discoverable.
const (
	legacyKeyVersion     = "version"
	legacyKeyAuth        = "auth_required"
	legacyKeyAuthMethods = "auth_methods"
	legacyKeyInstanceID  = "instance_id"
	legacyKeyNodeRole    = "node_role"
	legacyKeyOpenAI      = "api_openai"
	legacyKeyAnthropic   = "api_anthropic"
	legacyKeyGemini      = "api_gemini"
)

// protocolRoute maps a protocol token to its standard route below the gateway mount path.
type protocolRoute struct {
	version     string // Versioned prefix below path, e.g. /v1 or /v1beta
	suffix      string // Operation route below the versioned prefix
	legacyField string // Legacy api_* key that carried the versioned base path
}

var protocolRoutes = map[string]protocolRoute{
	"chat-completions": {version: "/v1", suffix: "/chat/completions", legacyField: legacyKeyOpenAI},
	"completions":      {version: "/v1", suffix: "/completions", legacyField: legacyKeyOpenAI},
	"responses":        {version: "/v1", suffix: "/responses", legacyField: legacyKeyOpenAI},
	"responses-ws":     {version: "/v1", suffix: "/responses", legacyField: legacyKeyOpenAI},
	"realtime":         {version: "/v1", suffix: "/realtime", legacyField: legacyKeyOpenAI},
	"images":           {version: "/v1", suffix: "/images/generations", legacyField: legacyKeyOpenAI},
	"videos":           {version: "/v1", suffix: "/videos", legacyField: legacyKeyOpenAI},
	"audio":            {version: "/v1", suffix: "/audio/speech", legacyField: legacyKeyOpenAI},
	"search":           {version: "/v1", suffix: "/alpha/search", legacyField: legacyKeyOpenAI},
	"messages":         {version: "/v1", suffix: "/messages", legacyField: legacyKeyAnthropic},
	"count-tokens":     {version: "/v1", suffix: "/messages/count_tokens", legacyField: legacyKeyAnthropic},
	"generate-content": {version: "/v1beta", suffix: "/models/{model}:generateContent", legacyField: legacyKeyGemini},
	"interactions":     {version: "/v1beta", suffix: "/interactions", legacyField: legacyKeyGemini},
}

func sanitizeTXTValue(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// TXTOptions contains input fields for generating DNS-SD TXT records.
type TXTOptions struct {
	Path         string
	Product      string
	Protocols    []string
	TLS          bool
	AuthRequired bool
	AuthMethods  []string
	InstanceID   string
	NodeRole     string
}

// DefaultTXTOptions provides safe defaults for CPA TXT records.
func DefaultTXTOptions() TXTOptions {
	return TXTOptions{
		Path:         defaultMountPath,
		Product:      ProductCPA,
		Protocols:    []string{"chat-completions", "responses", "messages", "generate-content", "interactions"},
		TLS:          false,
		AuthRequired: true,
		AuthMethods:  []string{"api_key"},
		NodeRole:     "standalone",
	}
}

// BuildTXTRecords constructs the draft-su-ai-gateway-discovery-00 TXT record.
// Required attributes are always emitted first; optional attributes are skipped
// when they would exceed the 255-byte string or 400-byte record budget.
func BuildTXTRecords(opts TXTOptions) ([]string, error) {
	path := opts.Path
	if path == "" {
		path = defaultMountPath
	}
	if validateMountPath(path) == "" {
		return nil, fmt.Errorf("discovery: invalid TXT path %q", path)
	}

	records := []string{
		txtKeyVersion + "=" + txtSchemaVersion,
		txtKeyPath + "=" + path,
		txtKeyTLS + "=" + txtFlag(opts.TLS),
		txtKeyAuth + "=" + txtFlag(opts.AuthRequired),
	}
	totalBytes := 0
	for _, record := range records {
		if len(record) > maxTXTRecordBytes {
			return nil, fmt.Errorf("discovery: TXT record %q exceeds %d bytes", record, maxTXTRecordBytes)
		}
		totalBytes += len(record) + 1 // +1 for the DNS character-string length prefix
	}
	if totalBytes > maxTXTBytes {
		return nil, fmt.Errorf("discovery: required TXT records exceed %d bytes", maxTXTBytes)
	}

	optional := []struct {
		key   string
		value string
	}{
		{txtKeyProtocols, joinTXTTokens(opts.Protocols)},
		{txtKeyAuthType, joinTXTTokens(opts.AuthMethods)},
		{txtKeyProduct, sanitizeTXTValue(opts.Product)},
		{txtKeyID, sanitizeTXTValue(opts.InstanceID)},
		{txtKeyRole, sanitizeTXTValue(opts.NodeRole)},
	}
	for _, item := range optional {
		if item.value == "" {
			continue
		}
		record := item.key + "=" + item.value
		cost := len(record) + 1
		if len(record) > maxTXTRecordBytes || totalBytes+cost > maxTXTBytes {
			continue
		}
		records = append(records, record)
		totalBytes += cost
	}
	return records, nil
}

func txtFlag(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

// parseTXTFlag accepts the draft boolean encoding ("0" or "1") only.
func parseTXTFlag(value string) (bool, bool) {
	switch value {
	case "1":
		return true, true
	case "0":
		return false, true
	default:
		return false, false
	}
}

func joinTXTTokens(values []string) string {
	tokens := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		token := strings.ToLower(strings.TrimSpace(value))
		if !validTXTToken(token) {
			continue
		}
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}
	return strings.Join(tokens, ",")
}

func validTXTToken(token string) bool {
	if token == "" {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ParseTXTRecords parses "key=value" strings into a map with lowercase keys.
// Per RFC 6763 §6.4, only the first occurrence of a key is used.
func ParseTXTRecords(txt []string) map[string]string {
	result := make(map[string]string, len(txt))
	for _, entry := range txt {
		key, value, _ := strings.Cut(entry, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, exists := result[key]; exists {
			continue
		}
		result[key] = value
	}
	return result
}

// validateMountPath returns p when it is a safe absolute path for the TXT path attribute.
// It rejects authorities, queries, fragments, escapes, traversal segments, and trailing slashes.
func validateMountPath(p string) string {
	if p == defaultMountPath {
		return p
	}
	if len(p) < 2 || p[0] != '/' || p[len(p)-1] == '/' {
		return ""
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c <= ' ' || c >= 0x7f || c == '?' || c == '#' || c == '\\' || c == '%' {
			return ""
		}
	}
	for _, segment := range strings.Split(p[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
	}
	return p
}

// joinURLPath appends route below base without producing a double slash.
func joinURLPath(base, route string) string {
	if base == defaultMountPath {
		return route
	}
	return base + route
}

// applyTXTMetadata fills svc from parsed TXT attributes and reports whether the
// record is usable. Draft records (txtvers) are validated strictly; legacy CPA
// records (version + api_*) remain accepted for mixed-version networks.
func applyTXTMetadata(svc *DiscoveredService, parsed map[string]string) bool {
	svc.RawTXT = parsed
	svc.Product = parsed[txtKeyProduct]
	svc.InstanceID = firstTXTValue(parsed, txtKeyID, legacyKeyInstanceID)
	svc.NodeRole = firstTXTValue(parsed, txtKeyRole, legacyKeyNodeRole)
	if protocols := parsed[txtKeyProtocols]; protocols != "" {
		svc.Protocols = parseTXTList(protocols)
	}
	if methods := firstTXTValue(parsed, txtKeyAuthType, legacyKeyAuthMethods); methods != "" {
		svc.AuthMethods = parseTXTList(methods)
	}
	tls, okTLS := parseTXTFlag(parsed[txtKeyTLS])
	svc.TLS = tls
	svc.Endpoints = make(map[string]string)

	if version, ok := parsed[txtKeyVersion]; ok {
		svc.Version = version
		auth, okAuth := parseTXTFlag(parsed[txtKeyAuth])
		path := validateMountPath(parsed[txtKeyPath])
		if version != txtSchemaVersion || !okTLS || !okAuth || path == "" {
			return false
		}
		svc.AuthRequired = auth
		svc.Path = path
		for _, protocol := range svc.Protocols {
			if route, known := protocolRoutes[protocol]; known {
				svc.Endpoints[protocol] = joinURLPath(path, route.version+route.suffix)
			}
		}
		return true
	}

	svc.Version = parsed[legacyKeyVersion]
	if svc.Version != txtSchemaVersion || !okTLS {
		return false
	}
	switch parsed[legacyKeyAuth] {
	case "true":
		svc.AuthRequired = true
	case "false":
		svc.AuthRequired = false
	default:
		return false
	}
	for _, protocol := range svc.Protocols {
		route, known := protocolRoutes[protocol]
		if !known {
			continue
		}
		if base := validateMountPath(parsed[route.legacyField]); base != "" {
			svc.Endpoints[protocol] = joinURLPath(base, route.suffix)
		}
	}
	return len(svc.Endpoints) > 0
}

func firstTXTValue(parsed map[string]string, keys ...string) string {
	for _, key := range keys {
		if value, ok := parsed[key]; ok {
			return value
		}
	}
	return ""
}

// ProtocolFromSubtype converts a DNS-SD subtype label (e.g. "_responses") to its protocol token.
func ProtocolFromSubtype(subtype string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(subtype), "_"))
}
