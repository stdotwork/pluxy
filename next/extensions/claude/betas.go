package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Betas a request body needs. Anthropic refuses some body features unless anthropic-beta names
// their beta, so, as today's core does for a client it does not make look like Claude Code, every
// request (a Messages client's own body or a translated one) gets the betas its body calls for,
// after the client's own list and without repeats:
//
//   - advisor-tool-2026-03-01 for a tool whose type starts with advisor_;
//   - the betas a body lists in a top-level betas field, which Messages does not take in the
//     body: the field is removed and its betas sent in the header;
//   - extended-cache-ttl-2025-04-11 for a cache_control marker with ttl "1h" (not on a token
//     count);
//   - thinking-binding-controls-2026-08-01 for thinking.block_binding.
//
// A client's own betas are never removed.
const (
	advisorBeta          = "advisor-tool-2026-03-01"
	extendedCacheTTLBeta = "extended-cache-ttl-2025-04-11"
	thinkingBindingBeta  = "thinking-binding-controls-2026-08-01"
)

// bodyBetas returns the betas body needs, in the order today's core adds them, and body without
// a betas field. count is true for a token count. A body that is not a JSON object is returned
// as it is, with no betas.
func bodyBetas(body []byte, count bool) ([]byte, []string) {
	// A body with none of these fields needs no beta; skip decoding it. (An advisor tool's type
	// is matched ignoring case and spaces, so look for tools, not the type.)
	if !bytes.Contains(body, []byte(`"tools"`)) && !bytes.Contains(body, []byte(`"betas"`)) &&
		!bytes.Contains(body, []byte(`"1h"`)) && !bytes.Contains(body, []byte(`"block_binding"`)) {
		return body, nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body, nil
	}
	var betas []string
	var tools []cacheable
	_ = json.Unmarshal(m["tools"], &tools)
	for _, t := range tools {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Type)), "advisor_") {
			betas = append(betas, advisorBeta)
			break
		}
	}
	if raw, ok := m["betas"]; ok {
		// As today's core reads it: each item of a list, or the one value, as text (see
		// betaText).
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			list = []json.RawMessage{raw}
		}
		for _, item := range list {
			if b := betaText(item); b != "" {
				betas = append(betas, b)
			}
		}
		delete(m, "betas")
		// The rest of the body is written back as the client sent it: values verbatim, without
		// the HTML escaping json.Marshal would add inside strings.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if enc.Encode(m) == nil {
			body = bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		}
	}
	if !count && hasHourTTL(m, tools) {
		betas = append(betas, extendedCacheTTLBeta)
	}
	var thinking map[string]json.RawMessage
	if json.Unmarshal(m["thinking"], &thinking) == nil {
		if _, ok := thinking["block_binding"]; ok {
			betas = append(betas, thinkingBindingBeta)
		}
	}
	return body, betas
}

// betaText is one item of a body's betas list as text: a string's value, or a number or boolean
// as written. Null, an object or a list is nothing, and so is text with a control character,
// which no header can carry: the client's own request would fail on it, not the account.
func betaText(item json.RawMessage) string {
	var t string
	var v any
	if json.Unmarshal(item, &v) != nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		t = strings.TrimSpace(v)
	case float64, bool:
		t = strings.TrimSpace(string(item))
	}
	if strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return t
}

// cacheable is a tool or a block, as far as betas care: its type and cache marker.
type cacheable struct {
	Type         string          `json:"type"`
	CacheControl json.RawMessage `json:"cache_control"`
}

// hasHourTTL reports whether a tool, a system block or a message content block carries
// cache_control with ttl "1h".
func hasHourTTL(m map[string]json.RawMessage, tools []cacheable) bool {
	hour := func(cc json.RawMessage) bool {
		var c struct {
			TTL string `json:"ttl"`
		}
		return json.Unmarshal(cc, &c) == nil && c.TTL == "1h"
	}
	for _, t := range tools {
		if hour(t.CacheControl) {
			return true
		}
	}
	var system []cacheable
	_ = json.Unmarshal(m["system"], &system)
	for _, b := range system {
		if hour(b.CacheControl) {
			return true
		}
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(m["messages"], &messages)
	for _, msg := range messages {
		var content []cacheable
		_ = json.Unmarshal(msg.Content, &content)
		for _, b := range content {
			if hour(b.CacheControl) {
				return true
			}
		}
	}
	return false
}

// addBetas adds each beta to the request's anthropic-beta list, after what is there, unless it
// is already listed.
func addBetas(list string, betas []string) string {
	for _, b := range betas {
		if b = strings.TrimSpace(b); b != "" {
			list = withBeta(list, b)
		}
	}
	return list
}
