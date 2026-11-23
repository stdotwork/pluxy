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

func TestScanTextDeltaMatchesDecoding(t *testing.T) {
	taken := 0
	for _, data := range []string{
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"delta":{"text":"a\nb\"\\é😀","type":"text_delta"},"index":12,"type":"content_block_delta"}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`,
		` {"type" : "content_block_delta" , "index":1 , "delta":{"type":"text_delta","text":"x"}} `,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"x"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x","extra":1}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":null}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":1}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a","text":"b"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"Type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":null}`,
		`{"type":"content_block_delta","index":1.0,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":-1,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":"1","delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":1234567890,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"\ud800"}}`,
		"{\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"bad\xff\"}}",
		`{"type":"content_block_delta","index":0,"index":1,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"},"usage":{}}`,
		`{"type":"content_block_start","index":0,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}`, `{}`, ``, `[]`,
	} {
		index, text, ok := scanTextDelta([]byte(data))
		if !ok {
			continue
		}
		taken++
		var ev streamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			t.Errorf("%q: scanTextDelta took an event encoding/json rejects: %v", data, err)
			continue
		}
		if ev.Type != "content_block_delta" || ev.Index != index || ev.Delta == nil || ev.Delta.Type != "text_delta" || ev.Delta.Text != text ||
			ev.Message != nil || ev.ContentBlock != nil || ev.Usage != nil || ev.Error != nil || ev.Delta.Thinking != "" || ev.Delta.PartialJSON != "" {
			t.Errorf("%q: scanTextDelta = %d, %q; decoding gives %+v %+v", data, index, text, ev, ev.Delta)
		}
	}
	if taken != 4 {
		t.Errorf("scanTextDelta took %d of the events, want the 4 well-formed ones", taken)
	}
}
