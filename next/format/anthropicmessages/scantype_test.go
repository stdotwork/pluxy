package anthropicmessages

import (
	"encoding/json"
	"math/rand"
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

// scanSeeds are Messages stream events, real ones and the odd ones above.
var scanSeeds = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello, \"world\"\né 😀"}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":15}}`,
	`{"type":"message_stop"}`, `{"type":"ping"}`,
	`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	`{"type":"message","id":"msg_1","content":[{"type":"text","text":"x"}]}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"},"Type":"x"}`,
}

// TestScansMatchDecodingOnEditedEvents compares scanTextDelta with decoding into streamEvent, and
// scanType with rawType's decoding, on every seed and many random edits of them: bytes changed,
// dropped and added, a key spelled in another case, an object cut short.
func TestScansMatchDecodingOnEditedEvents(t *testing.T) {
	seeds := append([]string(nil), scanSeeds...)
	seeds = append(seeds,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		`{"delta":{"text":"a\nb","type":"text_delta"},"index":12,"type":"content_block_delta"}`)
	rng := rand.New(rand.NewSource(11))
	junk := []byte("\"{}[],:\\ 0n\xff\x00ueEtT-.")
	cases, textTaken, typeTaken := 0, 0, 0
	check := func(data string) {
		cases++
		if index, text, ok := scanTextDelta([]byte(data)); ok {
			textTaken++
			var ev streamEvent
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				t.Fatalf("%q: scanTextDelta took an event encoding/json rejects: %v", data, err)
			}
			if ev.Type != "content_block_delta" || ev.Index != index || ev.Delta == nil || ev.Delta.Type != "text_delta" || ev.Delta.Text != text ||
				ev.Message != nil || ev.ContentBlock != nil || ev.Usage != nil || ev.Error != nil || ev.Delta.Thinking != "" || ev.Delta.PartialJSON != "" ||
				ev.Delta.Signature != "" || ev.Delta.Citation != nil || ev.Delta.StopReason != "" {
				t.Fatalf("%q: scanTextDelta = %d, %q; decoding gives %+v %+v", data, index, text, ev, ev.Delta)
			}
		}
		if got, want := rawType(json.RawMessage(data)), rawTypeDecoded(json.RawMessage(data)); got != want {
			t.Fatalf("rawType(%q) = %q, decoding gives %q", data, got, want)
		}
		if _, ok := scanType([]byte(data)); ok {
			typeTaken++
		}
	}
	for _, s := range seeds {
		check(s)
		for i := 0; i < 1500; i++ {
			e := []byte(s)
			for n := 1 + rng.Intn(3); n > 0 && len(e) > 0; n-- {
				at := rng.Intn(len(e))
				switch rng.Intn(5) {
				case 0:
					e = append(e[:at], e[at+1:]...)
				case 1:
					e = append(e[:at], append([]byte{junk[rng.Intn(len(junk))]}, e[at:]...)...)
				case 2:
					e[at] = junk[rng.Intn(len(junk))]
				case 3:
					if c := e[at]; c >= 'a' && c <= 'z' {
						e[at] = c - 32
					}
				default:
					e = e[:at]
				}
			}
			check(string(e))
		}
	}
	if textTaken == 0 || typeTaken == 0 {
		t.Fatalf("the scans took %d and %d of the events: the test covers nothing", textTaken, typeTaken)
	}
	t.Logf("%d events compared; scanTextDelta took %d, scanType %d", cases, textTaken, typeTaken)
}
