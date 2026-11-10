package main

import (
	"bytes"
	"encoding/json"
)

// emptyUserTurn is the turn Google needs where a request has no user turn to start or end with.
const emptyUserTurn = `{"role":"user","parts":[{"text":""}]}`

// fixTurns makes Google accept requests whose turns it would refuse, as today's core does for
// every request it sends to the Gemini API or Vertex AI, whatever the client's format (a Gemini
// client's own request included):
//
//   - a request whose first turn is the model's gets an empty user turn before it;
//   - a request whose last turn is the model's gets an empty user turn after it, except a last
//     turn made of function responses, which Google answers; a function response that carries an
//     image or file is not answered, so it gets the user turn too;
//   - on Vertex AI, trailing model turns made only of thoughts (a replayed reasoning item) are
//     dropped first, keeping at least one turn. In a request translated from another format, a
//     replayed reasoning item reaches Google as an empty text part carrying only its thought
//     signature (geminigenerate.EncodeRequest), which counts as a thought here.
//
// The leading turn is added before the thought turns are dropped, except in a stream, where it is
// added after, as today's core orders them. That only matters for a request that is a single
// thought turn: a stream sends it between two empty user turns, a reply that is not streamed
// sends just one empty user turn.
//
// A body that is not a JSON object with a contents array is returned as it is, and so is one
// that needs no change, byte for byte.
func fixTurns(body []byte, vertex, stream, translated bool) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	var contents []json.RawMessage
	if json.Unmarshal(m["contents"], &contents) != nil || len(contents) == 0 {
		return body
	}
	changed := false
	lead := func() {
		if turnRole(contents[0]) == "model" {
			contents = append([]json.RawMessage{json.RawMessage(emptyUserTurn)}, contents...)
			changed = true
		}
	}
	drop := func() {
		if !vertex {
			return
		}
		keep := len(contents)
		for keep > 1 && thoughtOnlyModelTurn(contents[keep-1], translated) {
			keep--
		}
		if keep < len(contents) {
			contents = contents[:keep]
			changed = true
		}
	}
	if stream {
		drop()
		lead()
	} else {
		lead()
		drop()
	}
	if last := contents[len(contents)-1]; endsWithUnansweredModelTurn(last) {
		contents = append(contents, json.RawMessage(emptyUserTurn))
		changed = true
	}
	if !changed {
		return body
	}
	return setContents(body, m, contents)
}

// fixLeadingTurn is fixTurns for a countTokens body, where today's core adds only the leading
// user turn, and only to a top-level contents array (a request wrapped in generateContentRequest
// is left as it is).
func fixLeadingTurn(body []byte) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	var contents []json.RawMessage
	if json.Unmarshal(m["contents"], &contents) != nil || len(contents) == 0 || turnRole(contents[0]) != "model" {
		return body
	}
	contents = append([]json.RawMessage{json.RawMessage(emptyUserTurn)}, contents...)
	return setContents(body, m, contents)
}

// setContents returns body with its contents replaced, every other field as it came.
func setContents(body []byte, m map[string]json.RawMessage, contents []json.RawMessage) []byte {
	raw, err := marshalRaw(contents)
	if err != nil {
		return body
	}
	m["contents"] = raw
	out, err := marshalRaw(m)
	if err != nil {
		return body
	}
	return out
}

// marshalRaw encodes v without escaping <, > and &, so raw JSON text keeps its bytes.
func marshalRaw(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

// turnRole returns a contents entry's role.
func turnRole(raw json.RawMessage) string {
	var t struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &t)
	return t.Role
}

// turnParts returns the parts of a contents entry, each as its fields; a part that is not an
// object has none.
func turnParts(raw json.RawMessage) []map[string]json.RawMessage {
	var t struct {
		Parts []json.RawMessage `json:"parts"`
	}
	_ = json.Unmarshal(raw, &t)
	parts := make([]map[string]json.RawMessage, len(t.Parts))
	for i, p := range t.Parts {
		_ = json.Unmarshal(p, &parts[i])
	}
	return parts
}

func isModel(role string) bool { return role == "model" || role == "assistant" }

// thoughtOnlyModelTurn reports whether a turn is the model's and every part of it is a thought,
// none a function call or response. With signatureOnly, a part that is an empty text and a thought
// signature, nothing else, is a thought too.
func thoughtOnlyModelTurn(raw json.RawMessage, signatureOnly bool) bool {
	if !isModel(turnRole(raw)) {
		return false
	}
	parts := turnParts(raw)
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts {
		_, call := p["functionCall"]
		_, resp := p["functionResponse"]
		if call || resp {
			return false
		}
		if string(p["thought"]) == "true" {
			continue
		}
		if !(signatureOnly && len(p) == 2 && string(p["text"]) == `""` && len(p["thoughtSignature"]) > 0) {
			return false
		}
	}
	return true
}

// endsWithUnansweredModelTurn reports whether a last turn needs a user turn after it: it is the
// model's, and not made to be answered by the model (a function response without an image or
// file).
func endsWithUnansweredModelTurn(raw json.RawMessage) bool {
	if !isModel(turnRole(raw)) {
		return false
	}
	response, media := false, false
	for _, p := range turnParts(raw) {
		fr, ok := p["functionResponse"]
		if !ok {
			continue
		}
		response = true
		var nested struct {
			Parts []map[string]json.RawMessage `json:"parts"`
		}
		_ = json.Unmarshal(fr, &nested)
		for _, n := range nested.Parts {
			for _, k := range []string{"inlineData", "inline_data", "fileData", "file_data"} {
				if _, ok := n[k]; ok {
					media = true
				}
			}
		}
	}
	return !response || media
}
