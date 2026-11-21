package openaichat

import "encoding/json"

// textMeta is what a text delta's raw holds when the reply came from this format: the upstream
// choice's logprobs, and whether the text is the reply's refusal rather than its content. Only a
// client of this format receives an event's raw.
type textMeta struct {
	Logprobs json.RawMessage `json:"logprobs,omitempty"`
	Refusal  bool            `json:"refusal,omitempty"`
}

// raw encodes meta as a text delta's raw, or returns nil when it holds nothing.
func (m textMeta) raw() json.RawMessage {
	if len(m.Logprobs) == 0 && !m.Refusal {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

// textMetaOf reads a text delta's raw.
func textMetaOf(raw json.RawMessage) textMeta {
	var m textMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}
