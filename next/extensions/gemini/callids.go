package main

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// vertexCallIDs reports whether a Vertex AI request translated from the client format from
// carries function call IDs, as today's core sends it: it removes the IDs a Responses client's
// calls carry (Vertex rejects them), and its Chat translation never writes a tool call's ID to any
// Gemini upstream. The IDs a Messages client's calls carry, and a Gemini client's own, are sent.
func vertexCallIDs(from string) bool {
	return from != llm.FormatOpenAIResponses && from != llm.FormatOpenAIChat
}

// stripCallIDs removes functionCall.id and functionResponse.id from the parts of a GenerateContent
// body's contents. A body with no such ID, or that is not shaped like a request, is returned
// as it is; in a body that changes, the bytes of every field that is not rewritten are kept.
func stripCallIDs(body []byte) []byte {
	dropID := func(fn json.RawMessage) (json.RawMessage, bool) {
		return editObject(fn, "id", func(json.RawMessage) (json.RawMessage, bool) { return nil, true })
	}
	out, changed := editObject(body, "contents", func(contents json.RawMessage) (json.RawMessage, bool) {
		return editArray(contents, func(turn json.RawMessage) (json.RawMessage, bool) {
			return editObject(turn, "parts", func(parts json.RawMessage) (json.RawMessage, bool) {
				return editArray(parts, func(part json.RawMessage) (json.RawMessage, bool) {
					part, call := editObject(part, "functionCall", dropID)
					part, resp := editObject(part, "functionResponse", dropID)
					return part, call || resp
				})
			})
		})
	})
	if !changed {
		return body
	}
	return out
}

// editObject rewrites the value of key in the JSON object raw with fix, which returns the new
// value (nil to remove the key) and whether it changed anything. It returns raw itself when raw is
// not an object, has no such key, or fix changed nothing.
func editObject(raw json.RawMessage, key string, fix func(json.RawMessage) (json.RawMessage, bool)) (json.RawMessage, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return raw, false
	}
	val, ok := obj[key]
	if !ok {
		return raw, false
	}
	val, changed := fix(val)
	if !changed {
		return raw, false
	}
	if val == nil {
		delete(obj, key)
	} else {
		obj[key] = val
	}
	return encodeJSON(raw, obj)
}

// editArray rewrites each element of the JSON array raw with fix, as editObject does.
func editArray(raw json.RawMessage, fix func(json.RawMessage) (json.RawMessage, bool)) (json.RawMessage, bool) {
	var elems []json.RawMessage
	if json.Unmarshal(raw, &elems) != nil {
		return raw, false
	}
	touched := false
	for i, e := range elems {
		if v, changed := fix(e); changed {
			elems[i], touched = v, true
		}
	}
	if !touched {
		return raw, false
	}
	return encodeJSON(raw, elems)
}

// encodeJSON encodes v without escaping <, > and &, so raw JSON text keeps its bytes. If v cannot
// be encoded it returns orig, unchanged.
func encodeJSON(orig json.RawMessage, v any) (json.RawMessage, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return orig, false
	}
	return bytes.TrimSpace(buf.Bytes()), true
}
