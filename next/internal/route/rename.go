package route

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// Renamed returns req as it goes to an account that knows its model as upstream: the model
// renamed, in the request and in the client's body when the body names it (a top-level
// "model"), so a body passed through unchanged asks for the same model. Everything else in the
// body stays byte for byte.
func Renamed(req llm.Request, upstream string) llm.Request {
	if upstream == "" || upstream == req.Model {
		return req
	}
	req.Model = upstream
	if req.Native != nil {
		if body, ok := setModel(req.Native.Body, upstream); ok {
			n := *req.Native
			n.Body = body
			req.Native = &n
		}
	}
	return req
}

// setModel replaces the value of each top-level "model" member of the JSON object body with
// model (a body may repeat a key; readers take the last). It reports false, leaving body alone,
// when body is not an object or has no such member.
func setModel(body json.RawMessage, model string) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	type span struct{ start, end int }
	var spans []span
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		if key != "model" {
			continue
		}
		end := int(dec.InputOffset())
		start := end - len(v)
		if start < 0 || !bytes.Equal(body[start:end], v) {
			return nil, false
		}
		spans = append(spans, span{start, end})
	}
	if len(spans) == 0 {
		return nil, false
	}
	name, err := json.Marshal(model)
	if err != nil {
		return nil, false
	}
	out := make([]byte, 0, len(body)+len(spans)*len(name))
	at := 0
	for _, sp := range spans {
		out = append(append(out, body[at:sp.start]...), name...)
		at = sp.end
	}
	return append(out, body[at:]...), true
}
