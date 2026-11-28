package openaichat

import "encoding/json"

type textMeta struct {
	Logprobs json.RawMessage `json:"logprobs,omitempty"`
	Refusal  bool            `json:"refusal,omitempty"`
}

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

func textMetaOf(raw json.RawMessage) textMeta {
	var m textMeta
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	return m
}
