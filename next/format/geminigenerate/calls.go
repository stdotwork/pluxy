package geminigenerate

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const MalformedFunctionCall = "MALFORMED_FUNCTION_CALL"

func malformedCall(message string) *llm.Error {
	msg := "Gemini ended the reply with finish reason " + MalformedFunctionCall + ": the model wrote a function call Gemini could not parse"
	if message != "" {
		msg += " (" + message + ")"
	}
	return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeRequest, ProviderCode: MalformedFunctionCall, Message: msg}
}

func IsMalformedCall(err error) bool {
	var e *llm.Error
	return errors.As(err, &e) && e.ProviderCode == MalformedFunctionCall
}

func callIDPrefix(responseID string) string {
	var b strings.Builder
	for _, r := range responseID {
		if r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
		if b.Len() == 64 {
			break
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	var r [8]byte
	_, _ = rand.Read(r[:])
	return hex.EncodeToString(r[:])
}

type fnRef struct{ id, name string }

// The two spellings Gemini takes for each part (its JSON uses lowerCamelCase; protobuf JSON
// parsing also accepts the field names).
var (
	callKeys     = []string{"functionCall", "function_call"}
	responseKeys = []string{"functionResponse", "function_response"}
)

func fnOf(part json.RawMessage, keys []string) (string, fnRef) {
	m := rawjson.Object(part)
	for _, k := range keys {
		var f struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if raw, ok := m[k]; ok && rawjson.Object(raw) != nil && json.Unmarshal(raw, &f) == nil {
			return k, fnRef{f.ID, f.Name}
		}
	}
	return "", fnRef{}
}

func callsOf(parts []json.RawMessage) []fnRef {
	var calls []fnRef
	for _, p := range parts {
		if k, c := fnOf(p, callKeys); k != "" {
			calls = append(calls, c)
		}
	}
	return calls
}

func orderResponses(calls []fnRef, parts []json.RawMessage) ([]json.RawMessage, bool) {
	var slots []int
	var keys []string
	var refs []fnRef
	for i, p := range parts {
		if k, r := fnOf(p, responseKeys); k != "" {
			slots, keys, refs = append(slots, i), append(keys, k), append(refs, r)
		}
	}
	if len(slots) == 0 || len(calls) == 0 {
		return parts, false
	}
	match := make([]int, len(refs))
	used := make([]bool, len(calls))
	take := func(i int, ok func(fnRef) bool) {
		for j, c := range calls {
			if !used[j] && ok(c) {
				used[j], match[i] = true, j
				return
			}
		}
	}
	for i := range match {
		match[i] = -1
		if id := refs[i].id; id != "" {
			take(i, func(c fnRef) bool { return c.id == id })
		}
	}
	for i, r := range refs {
		if match[i] < 0 && r.name != "" {
			take(i, func(c fnRef) bool { return c.name == r.name })
		}
	}
	for i, r := range refs {
		if match[i] < 0 && r.name == "" {
			take(i, func(fnRef) bool { return true })
		}
	}
	order := make([]int, len(refs))
	for i := range order {
		order[i] = i
	}
	rank := func(i int) int {
		if match[i] >= 0 {
			return match[i]
		}
		return len(calls) + i
	}
	sort.SliceStable(order, func(a, b int) bool { return rank(order[a]) < rank(order[b]) })

	out := append([]json.RawMessage(nil), parts...)
	changed := false
	for k, src := range order {
		p := parts[slots[src]]
		if refs[src].name == "" && match[src] >= 0 && calls[match[src]].name != "" {
			if named, ok := withName(p, keys[src], calls[match[src]].name); ok {
				p, changed = named, true
			}
		}
		if src != k {
			changed = true
		}
		out[slots[k]] = p
	}
	if !changed {
		return parts, false
	}
	return out, true
}

func withName(part json.RawMessage, key, name string) (json.RawMessage, bool) {
	m := rawjson.Object(part)
	fr := rawjson.Object(m[key])
	if m == nil || fr == nil {
		return part, false
	}
	fr["name"], _ = json.Marshal(name)
	b, err := marshalPlain(fr)
	if err != nil {
		return part, false
	}
	m[key] = b
	if b, err = marshalPlain(m); err != nil {
		return part, false
	}
	return b, true
}

func marshalPlain(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func OrderFunctionResponses(contents []json.RawMessage) ([]json.RawMessage, bool) {
	var out []json.RawMessage
	for i := 1; i < len(contents); i++ {
		prev, cur := rawjson.Object(contents[i-1]), rawjson.Object(contents[i])
		if prev == nil || cur == nil || !isModelRole(prev["role"]) || isModelRole(cur["role"]) {
			continue
		}
		var calls, parts []json.RawMessage
		if json.Unmarshal(prev["parts"], &calls) != nil || json.Unmarshal(cur["parts"], &parts) != nil {
			continue
		}
		parts, changed := orderResponses(callsOf(calls), parts)
		if !changed {
			continue
		}
		raw, err := marshalPlain(parts)
		if err != nil {
			continue
		}
		cur["parts"] = raw
		entry, err := marshalPlain(cur)
		if err != nil {
			continue
		}
		if out == nil {
			out = append([]json.RawMessage(nil), contents...)
		}
		out[i] = entry
	}
	if out == nil {
		return contents, false
	}
	return out, true
}

func isModelRole(raw json.RawMessage) bool {
	var role string
	_ = json.Unmarshal(raw, &role)
	return role == "model" || role == "assistant"
}
