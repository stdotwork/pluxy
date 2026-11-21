// Package rawjson keeps the JSON fields a format reads into the model apart from the ones it
// does not, and writes the latter back when it encodes the same format again.
package rawjson

import (
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/internal/jsonscan"
	"github.com/stdotwork/pluxy/next/llm"
)

// Object reads raw as a JSON object, or returns nil when it is not one, as json.Unmarshal into
// a map reads it. The values are slices of raw (each with no room after it, so appending to one
// copies it), or copies when a name sends the object to encoding/json: raw must not change while
// they are in use, and a value must not be changed in place.
func Object(raw []byte) map[string]json.RawMessage {
	members, ok := jsonscan.Object(raw)
	if !ok {
		return nil
	}
	m := make(map[string]json.RawMessage, len(members))
	for _, mem := range members {
		k, ok := jsonscan.Unquote(mem.Key)
		if !ok {
			// A name encoding/json repairs (invalid UTF-8): let it read the object.
			var m map[string]json.RawMessage
			_ = json.Unmarshal(raw, &m)
			return m
		}
		m[k] = mem.Value
	}
	return m
}

// Rest returns the fields of m other than modelled, as one JSON object, or nil when there are
// none.
func Rest(m map[string]json.RawMessage, modelled ...string) json.RawMessage {
	rest := map[string]json.RawMessage{}
	for k, v := range m {
		if !slices.Contains(modelled, k) {
			rest[k] = v
		}
	}
	return Encode(rest)
}

// Encode returns m as a JSON object with sorted keys, or nil when m is empty.
func Encode(m map[string]json.RawMessage) json.RawMessage {
	if len(m) == 0 {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// Origin returns an origin for an object at path in format, keeping its fields other than
// modelled; nested holds the unmodelled fields of child objects, merged in under their keys.
func Origin(format, path string, m map[string]json.RawMessage, modelled []string, nested map[string]json.RawMessage) *llm.Origin {
	rest := map[string]json.RawMessage{}
	for k, v := range m {
		if !slices.Contains(modelled, k) {
			rest[k] = v
		}
	}
	for k, v := range nested {
		if len(v) > 0 {
			rest[k] = v
		}
	}
	return &llm.Origin{Format: format, Path: path, Raw: Encode(rest)}
}

// Merge adds the fields of raw to the JSON object obj. A field obj already has keeps obj's value
// (the model's value wins over the kept one), except that two objects are merged field by field.
// When raw adds nothing, obj is returned as it is; otherwise the result has sorted keys.
func Merge(obj []byte, raw json.RawMessage) []byte {
	extra := Object(raw)
	if len(extra) == 0 {
		return obj
	}
	m := Object(obj)
	if m == nil {
		return obj
	}
	for k, v := range extra {
		cur, ok := m[k]
		switch {
		case !ok:
			m[k] = v
		case Object(cur) != nil && Object(v) != nil:
			m[k] = Merge(cur, v)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return obj
	}
	return b
}

// Losses returns one dropped loss per kept field of o when o came from a format other than to.
func Losses(o *llm.Origin, to string) []llm.Loss {
	if o == nil || o.Format == to || len(o.Raw) == 0 {
		return nil
	}
	m := Object(o.Raw)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]llm.Loss, 0, len(keys))
	for _, k := range keys {
		out = append(out, llm.Loss{Kind: llm.LossDropped, Path: Join(o.Path, k), Detail: "no place for it in " + to})
	}
	return out
}

// Text returns the string raw holds, and whether raw is a non-empty JSON string. A null, an empty
// string or any other value is not one: a format that reads a setting only from a string leaves
// the others where they are, in the request's kept fields.
func Text(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil || s == "" {
		return "", false
	}
	return s, true
}

// With returns modelled and the extra names, as a new list.
func With(modelled []string, extra ...string) []string {
	return slices.Concat(modelled, extra)
}

// Join joins a path and a key.
func Join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}
