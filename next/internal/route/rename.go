package route

import (
	"bytes"
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

func Renamed(req llm.Request, upstream string) llm.Request {
	if upstream == "" || upstream == req.Model {
		return req
	}
	req.Model = upstream
	if req.Native != nil {
		if body, ok := setModel(req.Native.Body, upstream); ok {
			n := *req.Native
			// Gemini's model is in the request path.
			if n.IsParsed() && n.Format != llm.FormatGeminiGenerate {
				n.Parsed = body
			}
			n.Body = body
			req.Native = &n
		}
	}
	return req
}

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
