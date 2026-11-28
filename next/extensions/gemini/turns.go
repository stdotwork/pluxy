package main

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
)

// emptyUserTurn is the turn Google needs where a request has no user turn to start or end with.
const emptyUserTurn = `{"role":"user","parts":[{"text":""}]}`

func fixTurns(body []byte, vertex, stream, translated bool) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	var contents []json.RawMessage
	if json.Unmarshal(m["contents"], &contents) != nil || len(contents) == 0 {
		return body
	}
	// Function responses go in the order of the calls they answer, and one with no name takes its
	// call's name (geminigenerate.OrderFunctionResponses): Gemini matches them by position.
	contents, changed := geminigenerate.OrderFunctionResponses(contents)
	lead := func() {
		if turnRole(contents[0]) == "model" {
			contents = append([]json.RawMessage{json.RawMessage(emptyUserTurn)}, contents...)
			changed = true
		}
	}
	// On Vertex AI, trailing model turns made only of thoughts (a replayed reasoning item) are
	// dropped first, keeping at least one turn.
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

func marshalRaw(v any) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func turnRole(raw json.RawMessage) string {
	var t struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(raw, &t)
	return t.Role
}

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

// A request whose last turn is the model's gets an empty user turn after it, except a last
// turn made of function responses, which Google answers; a function response that carries an
// image or file is not answered, so it gets the user turn too.
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
