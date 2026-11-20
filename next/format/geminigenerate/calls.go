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

// MalformedFunctionCall is the finish reason Gemini gives a reply whose function call the model
// wrote in a form Gemini could not parse (Google's GenerateContent reference, FinishReason).
const MalformedFunctionCall = "MALFORMED_FUNCTION_CALL"

// malformedCall returns the error for a reply that ended with MalformedFunctionCall; message is
// Gemini's finishMessage, which quotes the call.
func malformedCall(message string) *llm.Error {
	msg := "Gemini ended the reply with finish reason " + MalformedFunctionCall + ": the model wrote a function call Gemini could not parse"
	if message != "" {
		msg += " (" + message + ")"
	}
	return &llm.Error{Code: llm.CodeUpstream, Scope: llm.ScopeRequest, ProviderCode: MalformedFunctionCall, Message: msg}
}

// IsMalformedCall reports whether err is the error for a reply that ended with
// MALFORMED_FUNCTION_CALL.
func IsMalformedCall(err error) bool {
	var e *llm.Error
	return errors.As(err, &e) && e.ProviderCode == MalformedFunctionCall
}

// callIDPrefix returns the part of the IDs made for one reply's calls that Gemini sent without an
// ID: the reply's responseId, in the characters every client format takes in a call ID, or random
// when there is none. Gemini gives every reply its own responseId, so the IDs are unique across
// the replies of a conversation, not only within one.
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

// fnRef is the id and name of a function call or response.
type fnRef struct{ id, name string }

// The two spellings Gemini takes for each part (its JSON uses lowerCamelCase; protobuf JSON
// parsing also accepts the field names).
var (
	callKeys     = []string{"functionCall", "function_call"}
	responseKeys = []string{"functionResponse", "function_response"}
)

// fnOf returns the key and the id and name of part's function call or response, whichever keys
// names, or "" when part has none.
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

// callsOf returns the function calls among a model turn's parts, in order.
func callsOf(parts []json.RawMessage) []fnRef {
	var calls []fnRef
	for _, p := range parts {
		if k, c := fnOf(p, callKeys); k != "" {
			calls = append(calls, c)
		}
	}
	return calls
}

// orderResponses puts the function responses among parts, the turn after the model turn that
// made calls, in the order of the calls they answer, and gives a response with no name its call's
// name. Gemini matches responses to calls by position: Google's function calling guide says to
// "include the responses in the same order as they were requested", Vertex AI's says "in the exact
// same order as the functionCall parts returned by the model". A response is matched to its call by
// ID, then by name (in turn, for several calls to one function), then, when it has no name, to the
// first call left. Responses that match no call keep their order after the matched ones. Other
// parts keep their places; the responses fill the places responses held. It reports whether
// anything changed; parts that did not change keep their bytes.
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

// withName returns part with the name of its function response (under key) set to name.
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

// marshalPlain encodes v without escaping <, > and &, so raw JSON text keeps its bytes.
func marshalPlain(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// OrderFunctionResponses applies orderResponses to every turn of a GenerateContent body's
// contents that follows a model turn: function responses in the order of the calls they answer,
// each with a name. A Gemini client's own body gets this repair from the Gemini provider, as a
// body EncodeRequest builds has it already. It returns contents and whether anything changed; an
// entry that does not change keeps its bytes.
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

// isModelRole reports whether a turn's role (raw JSON) is the model's.
func isModelRole(raw json.RawMessage) bool {
	var role string
	_ = json.Unmarshal(raw, &role)
	return role == "model" || role == "assistant"
}
