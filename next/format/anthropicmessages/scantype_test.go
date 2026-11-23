package anthropicmessages

import (
	"encoding/json"
	"testing"
)

// rawTypeDecoded is rawType before scanType: the type field as encoding/json reads it.
func rawTypeDecoded(raw json.RawMessage) string {
	var e struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return e.Type
}

func TestRawTypeMatchesDecoding(t *testing.T) {
	for _, raw := range []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"index":0,"type":"message_start"}`, `{"type":"message","id":"x"}`, `{}`, `{"a":1}`, ` {"type":"x"}`,
		`{"Type":"x"}`, `{"TYPE":"x"}`, `{"type":"x"}`, `{"type":"a","type":"b"}`, `{"type":"a","type":1}`,
		`{"type":"a","Type":"b"}`, `{"type":1}`, `{"type":null}`, `{"type":"a","type":null}`, `{"type":["a"]}`,
		`{"type":"\ud800"}`, "{\"type\":\"bad\xff\"}", `{"type":"a\nb"}`, `{"type":"a"`, `{"type":}`, `[]`, `"type"`, ``,
		`{"tYpe":"q","x":{"type":"inner"}}`, `{"typé":"x"}`, `{"types":"x","text":"y"}`, `{"type":"x"} y`,
	} {
		if got, want := rawType(json.RawMessage(raw)), rawTypeDecoded(json.RawMessage(raw)); got != want {
			t.Errorf("rawType(%q) = %q, decoding gives %q", raw, got, want)
		}
	}
}
