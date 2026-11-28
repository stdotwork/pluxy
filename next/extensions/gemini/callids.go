package main

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

func vertexCallIDs(from string) bool {
	return from != llm.FormatOpenAIResponses && from != llm.FormatOpenAIChat
}

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

func encodeJSON(orig json.RawMessage, v any) (json.RawMessage, bool) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return orig, false
	}
	return bytes.TrimSpace(buf.Bytes()), true
}
