package openaichat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func renderAgg(t *testing.T, evs []llm.Event) ([]byte, []llm.Loss, int) {
	t.Helper()
	a := NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	b, err := a.Response()
	if err != nil {
		t.Fatalf("Aggregator.Response: %v", err)
	}
	return b, a.Losses(), a.Size()
}

func renderStream(t *testing.T, evs []llm.Event) (string, []llm.Loss) {
	t.Helper()
	var buf bytes.Buffer
	w := NewStreamWriter(&buf, true)
	for _, ev := range evs {
		if err := w.Write(ev); err != nil {
			t.Fatalf("StreamWriter.Write(%s): %v", ev.Type, err)
		}
	}
	return buf.String(), w.Losses()
}

func decodeStream(t *testing.T, chunks []string) ([]llm.Event, []llm.Loss) {
	t.Helper()
	d := NewStreamDecoder()
	var evs []llm.Event
	for _, ch := range chunks {
		e, err := d.Decode([]byte(ch))
		if err != nil {
			t.Fatalf("Decode(%s): %v", ch, err)
		}
		evs = append(evs, e...)
	}
	end, err := d.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	return append(evs, end...), d.Losses()
}

func checkEventsFitMaxFrame(t *testing.T, evs []llm.Event) {
	t.Helper()
	var seq llm.Sequence
	for i, ev := range evs {
		if err := seq.Check(ev); err != nil {
			t.Fatalf("event[%d] (%s) violates Sequence: %v", i, ev.Type, err)
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event[%d]: %v", i, err)
		}
		if len(b) > protocol.MaxFrame {
			t.Fatalf("event[%d] (%s) is %d bytes, exceeds protocol.MaxFrame (%d)", i, ev.Type, len(b), protocol.MaxFrame)
		}
	}
}

func TestSameFormatAnnotationsNonStreamAndStream(t *testing.T) {
	annJSON := `[{"type":"url_citation","url_citation":{"url":"https://example.com/a","title":"A","start_index":0,"end_index":5}},{"type":"vendor_note","note":"kept"}]`
	nonStreamBody := fmt.Sprintf(
		`{"id":"c1","object":"chat.completion","created":1,"model":"gpt-5-search-api","choices":[{"index":0,"message":{"role":"assistant","content":"hello world","annotations":%s},"finish_reason":"stop"}]}`,
		annJSON,
	)

	evs, losses, err := DecodeResponseLosses([]byte(nonStreamBody))
	if err != nil || len(losses) != 0 {
		t.Fatalf("DecodeResponseLosses: err=%v losses=%+v", err, losses)
	}
	checkEventsFitMaxFrame(t, evs)

	aggBody, aggLosses, aggSize := renderAgg(t, evs)
	if len(aggLosses) != 0 {
		t.Fatalf("agg losses = %+v, want none", aggLosses)
	}
	if aggSize < len(annJSON) {
		t.Errorf("aggSize = %d, want >= %d", aggSize, len(annJSON))
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content     *string         `json:"content"`
				Annotations json.RawMessage `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(aggBody, &got); err != nil {
		t.Fatalf("unmarshal aggBody: %v", err)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Content == nil || *got.Choices[0].Message.Content != "hello world" {
		t.Fatalf("unexpected message: %s", aggBody)
	}
	if string(got.Choices[0].Message.Annotations) != annJSON {
		t.Errorf("agg annotations = %s\nwant %s", got.Choices[0].Message.Annotations, annJSON)
	}

	sseOut, streamLosses := renderStream(t, evs)
	if len(streamLosses) != 0 {
		t.Fatalf("stream losses = %+v, want none", streamLosses)
	}
	if !strings.Contains(sseOut, `"annotations":`+annJSON) {
		t.Errorf("nonstream->stream SSE missing annotations:\n%s", sseOut)
	}

	// Streamed upstream with delta.annotations -> both StreamWriter and Aggregator keep them.
	streamEvs, sLosses := decodeStream(t, []string{
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5-search-api","choices":[{"index":0,"delta":{"role":"assistant","content":"hello world"}}]}`,
		fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-5-search-api","choices":[{"index":0,"delta":{"annotations":%s},"finish_reason":"stop"}]}`, annJSON),
		`[DONE]`,
	})
	if len(sLosses) != 0 {
		t.Fatalf("stream decode losses = %+v", sLosses)
	}
	checkEventsFitMaxFrame(t, streamEvs)

	sAggBody, sAggLosses, sAggSize := renderAgg(t, streamEvs)
	if len(sAggLosses) != 0 || sAggSize < len(annJSON) {
		t.Fatalf("stream->agg losses=%+v size=%d", sAggLosses, sAggSize)
	}
	got = struct {
		Choices []struct {
			Message struct {
				Content     *string         `json:"content"`
				Annotations json.RawMessage `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
	}{}
	if err := json.Unmarshal(sAggBody, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Choices[0].Message.Annotations) != annJSON {
		t.Errorf("stream->agg annotations = %s\nwant %s", got.Choices[0].Message.Annotations, annJSON)
	}

	sSSE, sStreamLosses := renderStream(t, streamEvs)
	if len(sStreamLosses) != 0 {
		t.Fatalf("stream->stream losses = %+v", sStreamLosses)
	}
	if !strings.Contains(sSSE, `"annotations":`+annJSON) {
		t.Errorf("stream->stream SSE missing annotations:\n%s", sSSE)
	}
}

func TestSameFormatAudioNonStreamAndStream(t *testing.T) {
	// 4 bytes ("AQIDBA==") has base64 padding, 3 bytes ("BQYH") does not: merging must yield valid base64 of all 7 bytes.
	part1 := base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4})
	part2 := base64.StdEncoding.EncodeToString([]byte{5, 6, 7})
	wantMerged := base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7})

	// 1. Non-stream upstream -> Aggregator and StreamWriter.
	nonStreamBody := fmt.Sprintf(
		`{"id":"c1","object":"chat.completion","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"message":{"role":"assistant","content":null,"audio":{"id":"audio_abc","data":%q,"expires_at":1730000000,"transcript":"Hello."}},"finish_reason":"stop"}]}`,
		part1,
	)
	evs, losses, err := DecodeResponseLosses([]byte(nonStreamBody))
	if err != nil || len(losses) != 0 {
		t.Fatalf("DecodeResponseLosses: err=%v losses=%+v", err, losses)
	}
	checkEventsFitMaxFrame(t, evs)
	// Verify EventMedia does not duplicate the base64 audio data inside Raw.
	for _, ev := range evs {
		if ev.Type == llm.EventMedia && bytes.Contains(ev.Raw, []byte(part1)) {
			t.Errorf("EventMedia.Raw duplicated audio data: %s", ev.Raw)
		}
	}

	aggBody, aggLosses, aggSize := renderAgg(t, evs)
	if len(aggLosses) != 0 {
		t.Fatalf("nonstream agg losses = %+v, want none", aggLosses)
	}
	if aggSize < len(part1) {
		t.Errorf("nonstream aggSize = %d, want >= %d", aggSize, len(part1))
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
				Audio   *struct {
					ID         string `json:"id"`
					Data       string `json:"data"`
					ExpiresAt  int64  `json:"expires_at"`
					Transcript string `json:"transcript"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(aggBody, &got); err != nil {
		t.Fatalf("unmarshal aggBody: %v", err)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Content != nil {
		t.Fatalf("expected content:null on audio-only reply, got %s", aggBody)
	}
	a := got.Choices[0].Message.Audio
	if a == nil || a.ID != "audio_abc" || a.Data != part1 || a.ExpiresAt != 1730000000 || a.Transcript != "Hello." {
		t.Fatalf("nonstream agg audio = %+v (body: %s)", a, aggBody)
	}

	sseOut, streamLosses := renderStream(t, evs)
	if len(streamLosses) != 0 {
		t.Fatalf("nonstream->stream losses = %+v, want none", streamLosses)
	}
	if !strings.Contains(sseOut, `"audio":{`) || !strings.Contains(sseOut, part1) || !strings.Contains(sseOut, `"audio_abc"`) || !strings.Contains(sseOut, `"Hello."`) {
		t.Errorf("nonstream->stream SSE missing audio:\n%s", sseOut)
	}

	// 2. Stream upstream with multiple audio chunks (including a data-less metadata chunk in the middle and at the end).
	streamEvs, sLosses := decodeStream(t, []string{
		fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"role":"assistant","audio":{"id":"audio_abc","data":%q,"transcript":"Hel"}},"finish_reason":null}]}`, part1),
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"audio":{"transcript":"lo"}},"finish_reason":null}]}`,
		fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"audio":{"data":%q}},"finish_reason":null}]}`, part2),
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"audio":{"transcript":".","expires_at":1730000000}},"finish_reason":null}]}`,
		`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	})
	if len(sLosses) != 0 {
		t.Fatalf("stream decode losses = %+v", sLosses)
	}
	checkEventsFitMaxFrame(t, streamEvs)

	sAggBody, sAggLosses, sAggSize := renderAgg(t, streamEvs)
	if len(sAggLosses) != 0 {
		t.Fatalf("stream->agg losses = %+v, want none", sAggLosses)
	}
	if sAggSize < len(part1)+len(part2) {
		t.Errorf("stream->agg size = %d, want >= %d", sAggSize, len(part1)+len(part2))
	}
	got = struct {
		Choices []struct {
			Message struct {
				Content *string `json:"content"`
				Audio   *struct {
					ID         string `json:"id"`
					Data       string `json:"data"`
					ExpiresAt  int64  `json:"expires_at"`
					Transcript string `json:"transcript"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}{}
	if err := json.Unmarshal(sAggBody, &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message.Content != nil {
		t.Fatalf("expected content:null on audio-only streamed reply, got %s", sAggBody)
	}
	a = got.Choices[0].Message.Audio
	if a == nil || a.ID != "audio_abc" || a.Data != wantMerged || a.ExpiresAt != 1730000000 || a.Transcript != "Hello." {
		t.Fatalf("stream->agg audio = %+v, want id=audio_abc data=%s expires_at=1730000000 transcript=Hello. (body: %s)", a, wantMerged, sAggBody)
	}

	sSSE, sStreamLosses := renderStream(t, streamEvs)
	if len(sStreamLosses) != 0 {
		t.Fatalf("stream->stream losses = %+v, want none", sStreamLosses)
	}
	for _, want := range []string{`"audio_abc"`, part1, `"Hel"`, `"lo"`, part2, `"."`, `1730000000`} {
		if !strings.Contains(sSSE, want) {
			t.Errorf("stream->stream SSE missing %s:\n%s", want, sSSE)
		}
	}
}

func TestSameFormatReplyWithoutAnnotationsOrAudioStaysByteIdentical(t *testing.T) {
	// Plain reply and reply with empty annotations: [] must both stay byte-identical.
	for _, tc := range []struct {
		name string
		body string
	}{
		{"plain", `{"id":"c1","object":"chat.completion","created":1,"model":"m","system_fingerprint":"fp_1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`},
		{"empty_annotations", `{"id":"c1","object":"chat.completion","created":1,"model":"m","system_fingerprint":"fp_1","choices":[{"index":0,"message":{"role":"assistant","content":"hi","annotations":[]},"finish_reason":"stop"}]}`},
		{"null_annotations_audio", `{"id":"c1","object":"chat.completion","created":1,"model":"m","system_fingerprint":"fp_1","choices":[{"index":0,"message":{"role":"assistant","content":"hi","annotations":null,"audio":null},"finish_reason":"stop"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs, losses, err := DecodeResponseLosses([]byte(tc.body))
			if err != nil || len(losses) != 0 {
				t.Fatalf("decode: err=%v losses=%+v", err, losses)
			}
			out, aggLosses, _ := renderAgg(t, evs)
			if len(aggLosses) != 0 {
				t.Fatalf("agg losses: %+v", aggLosses)
			}
			want := `{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"system_fingerprint":"fp_1"}`
			if string(out) != want {
				t.Errorf("out = %s\nwant %s", out, want)
			}
		})
	}
}

func TestSameFormatOversizedAnnotationsAndAudioMetaBounded(t *testing.T) {
	bigText := strings.Repeat("a", 5<<20) // 5 MiB > 4 MiB cap
	bigAnnBody := fmt.Sprintf(
		`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi","annotations":[{"type":"vendor","data":%q}]},"finish_reason":"stop"}]}`,
		bigText,
	)
	evs, losses, err := DecodeResponseLosses([]byte(bigAnnBody))
	if err != nil {
		t.Fatalf("big annotations decode err: %v", err)
	}
	checkEventsFitMaxFrame(t, evs)
	if len(losses) == 0 {
		t.Errorf("expected loss for oversized annotations, got none")
	}

	bigAudioBody := fmt.Sprintf(
		`{"id":"c1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":null,"audio":{"id":"a1","data":"AQID","transcript":%q}},"finish_reason":"stop"}]}`,
		bigText,
	)
	evs, losses, err = DecodeResponseLosses([]byte(bigAudioBody))
	if err != nil {
		t.Fatalf("big audio meta decode err: %v", err)
	}
	checkEventsFitMaxFrame(t, evs)
	if len(losses) == 0 {
		t.Errorf("expected loss for oversized audio metadata, got none")
	}
}
