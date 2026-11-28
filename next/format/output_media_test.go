package format_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

var (
	samplePNG = base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01"))
	sampleWAV = base64.StdEncoding.EncodeToString([]byte("RIFF\x24\x00\x00\x00WAVEfmt \x10\x00\x00\x00"))
	samplePCM = base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08})
	sampleSVG = base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
)

func roundTripMediaEvents(t *testing.T, evs []llm.Event) []llm.Event {
	t.Helper()
	out := make([]llm.Event, len(evs))
	var seq llm.Sequence
	for i, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal event[%d] (%s): %v", i, ev.Type, err)
		}
		if len(b) > protocol.MaxFrame {
			t.Fatalf("event[%d] (%s) is %d bytes, over protocol.MaxFrame (%d)", i, ev.Type, len(b), protocol.MaxFrame)
		}
		if err := json.Unmarshal(b, &out[i]); err != nil {
			t.Fatalf("unmarshal event[%d] (%s): %v", i, ev.Type, err)
		}
		if err := seq.Check(out[i]); err != nil {
			t.Fatalf("sequence check event[%d] (%s): %v", i, out[i].Type, err)
		}
	}
	return out
}

func renderGemini(t *testing.T, evs []llm.Event) (body []byte, sseOut string, aggLosses, streamLosses []llm.Loss, aggSize int) {
	t.Helper()
	a := geminigenerate.NewAggregator()
	var buf bytes.Buffer
	w := geminigenerate.NewStreamWriter(&buf)
	for _, ev := range evs {
		a.Add(ev)
		if err := w.Write(ev); err != nil {
			t.Fatalf("gemini stream write: %v", err)
		}
	}
	b, err := a.Response()
	if err != nil {
		t.Fatalf("gemini aggregate response: %v", err)
	}
	return b, buf.String(), a.Losses(), w.Losses(), a.Size()
}

func renderResponses(t *testing.T, evs []llm.Event) (body []byte, sseOut string, aggLosses, streamLosses []llm.Loss, aggSize int) {
	t.Helper()
	req := llm.Request{Model: "test-model"}
	a := openairesponses.NewAggregator(req)
	var buf bytes.Buffer
	w := openairesponses.NewStreamWriter(&buf, req)
	for _, ev := range evs {
		a.Add(ev)
		if err := w.Write(ev); err != nil {
			t.Fatalf("responses stream write: %v", err)
		}
	}
	b, err := a.Response()
	if err != nil {
		t.Fatalf("responses aggregate response: %v", err)
	}
	return b, buf.String(), a.Losses(), w.Losses(), a.Size()
}

func renderChat(t *testing.T, evs []llm.Event) (body []byte, sseOut string, aggLosses, streamLosses []llm.Loss, aggSize int) {
	t.Helper()
	a := openaichat.NewAggregator()
	var buf bytes.Buffer
	w := openaichat.NewStreamWriter(&buf, false)
	for _, ev := range evs {
		a.Add(ev)
		if err := w.Write(ev); err != nil {
			t.Fatalf("chat stream write: %v", err)
		}
	}
	b, err := a.Response()
	if err != nil {
		t.Fatalf("chat aggregate response: %v", err)
	}
	return b, buf.String(), a.Losses(), w.Losses(), a.Size()
}

func renderMessages(t *testing.T, evs []llm.Event) (body []byte, sseOut string, aggLosses, streamLosses []llm.Loss, aggSize int) {
	t.Helper()
	a := anthropicmessages.NewAggregator()
	var buf bytes.Buffer
	w := anthropicmessages.NewStreamWriter(&buf)
	for _, ev := range evs {
		a.Add(ev)
		if err := w.Write(ev); err != nil {
			t.Fatalf("messages stream write: %v", err)
		}
	}
	b, err := a.Response()
	if err != nil {
		t.Fatalf("messages aggregate response: %v", err)
	}
	return b, buf.String(), a.Losses(), w.Losses(), a.Size()
}

func hasLoss(losses []llm.Loss, prefix string) bool {
	for _, l := range losses {
		if strings.HasPrefix(l.Path, prefix) {
			return true
		}
	}
	return false
}

func assertNoMediaBytesInLosses(t *testing.T, losses []llm.Loss, samples ...string) {
	t.Helper()
	for _, l := range losses {
		for _, s := range samples {
			if s != "" && (strings.Contains(l.Path, s) || strings.Contains(l.Detail, s)) {
				t.Errorf("loss leaked media bytes: %+v", l)
			}
		}
	}
}

func TestGeminiImageToAllFormats(t *testing.T) {
	nonStreamBody := fmt.Sprintf(`{"responseId":"g1","modelVersion":"gemini-2.5-flash-image","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Before."},{"inlineData":{"mimeType":"image/png","data":%q}},{"text":"After."}]},"finishReason":"STOP"}]}`, samplePNG)
	streamChunks := []string{
		`{"responseId":"g1","modelVersion":"gemini-2.5-flash-image","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Before."}]}}]}`,
		fmt.Sprintf(`{"responseId":"g1","modelVersion":"gemini-2.5-flash-image","candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]}}]}`, samplePNG),
		`{"responseId":"g1","modelVersion":"gemini-2.5-flash-image","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"After."}]},"finishReason":"STOP"}]}`,
	}

	decode := func(stream bool) []llm.Event {
		if !stream {
			evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(nonStreamBody))
			if err != nil || len(losses) != 0 {
				t.Fatalf("non-stream decode: err=%v losses=%+v", err, losses)
			}
			return roundTripMediaEvents(t, evs)
		}
		d := geminigenerate.NewStreamDecoder()
		var evs []llm.Event
		for _, ch := range streamChunks {
			e, err := d.Decode([]byte(ch))
			if err != nil {
				t.Fatalf("stream decode: %v", err)
			}
			evs = append(evs, e...)
		}
		end, err := d.Close()
		if err != nil || len(d.Losses()) != 0 {
			t.Fatalf("stream close: err=%v losses=%+v", err, d.Losses())
		}
		return roundTripMediaEvents(t, append(evs, end...))
	}

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			evs := decode(stream)

			gBody, gSSE, gAL, gSL, _ := renderGemini(t, evs)
			if len(gAL) != 0 || len(gSL) != 0 {
				t.Errorf("gemini same-format losses: agg=%+v stream=%+v", gAL, gSL)
			}
			if !stream && string(gBody) != nonStreamBody {
				t.Errorf("gemini same-format non-stream changed:\ngot:  %s\nwant: %s", gBody, nonStreamBody)
			}
			if stream {
				wantSSE := "data: " + streamChunks[0] + "\n\ndata: " + streamChunks[1] + "\n\ndata: " + streamChunks[2] + "\n\n"
				if gSSE != wantSSE {
					t.Errorf("gemini same-format stream changed:\ngot:  %s\nwant: %s", gSSE, wantSSE)
				}
			}

			rBody, _, rAL, rSL, _ := renderResponses(t, evs)
			if !hasLoss(rAL, "media.image") || !hasLoss(rSL, "media.image") {
				t.Errorf("gemini->responses missing media.image loss: %+v / %+v", rAL, rSL)
			}
			if strings.Contains(string(rBody), "image_generation_call") {
				t.Errorf("gemini->responses invented an image item: %s", rBody)
			}
			if b, a := strings.Index(string(rBody), "Before."), strings.Index(string(rBody), "After."); b < 0 || a < b {
				t.Errorf("gemini->responses text out of order: %s", rBody)
			}

			cBody, cSSE, cAL, cSL, _ := renderChat(t, evs)
			if !hasLoss(cAL, "media.image") || !hasLoss(cSL, "media.image") {
				t.Errorf("gemini->chat missing media.image loss: agg=%+v stream=%+v", cAL, cSL)
			}
			if strings.Contains(string(cBody), samplePNG) || strings.Contains(cSSE, samplePNG) {
				t.Errorf("chat output leaked image bytes")
			}
			if !strings.Contains(string(cBody), "Before.After.") {
				t.Errorf("chat text changed: %s", cBody)
			}

			mBody, mSSE, mAL, mSL, _ := renderMessages(t, evs)
			if !hasLoss(mAL, "media.image") || !hasLoss(mSL, "media.image") {
				t.Errorf("gemini->messages missing media.image loss: agg=%+v stream=%+v", mAL, mSL)
			}
			if strings.Contains(string(mBody), samplePNG) || strings.Contains(mSSE, samplePNG) {
				t.Errorf("messages output leaked image bytes")
			}
			if !strings.Contains(string(mBody), "Before.") || !strings.Contains(string(mBody), "After.") {
				t.Errorf("messages text lost: %s", mBody)
			}
		})
	}
}

func TestGeminiAudioToAllFormats(t *testing.T) {
	body := fmt.Sprintf(`{"responseId":"ga1","modelVersion":"gemini-2.5-flash-tts","candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"audio/L16;codec=pcm;rate=24000","data":%q}}]},"finishReason":"STOP"}]}`, samplePCM)
	evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil || len(losses) != 0 {
		t.Fatalf("decode: err=%v losses=%+v", err, losses)
	}
	evs = roundTripMediaEvents(t, evs)

	gBody, _, gAL, gSL, _ := renderGemini(t, evs)
	if string(gBody) != body || len(gAL) != 0 || len(gSL) != 0 {
		t.Errorf("gemini audio same-format mismatch: body=%s losses=%+v/%+v", gBody, gAL, gSL)
	}

	for name, fn := range map[string]func(*testing.T, []llm.Event) ([]byte, string, []llm.Loss, []llm.Loss, int){
		"responses": renderResponses,
		"chat":      renderChat,
		"messages":  renderMessages,
	} {
		outBody, outSSE, al, sl, _ := fn(t, evs)
		if !hasLoss(al, "media.audio") || !hasLoss(sl, "media.audio") {
			t.Errorf("gemini audio -> %s missing media.audio loss: agg=%+v stream=%+v", name, al, sl)
		}
		if strings.Contains(string(outBody), samplePCM) || strings.Contains(outSSE, samplePCM) {
			t.Errorf("gemini audio -> %s leaked audio bytes", name)
		}
	}
}

func TestResponsesImageToAllFormats(t *testing.T) {
	nonStreamBody := fmt.Sprintf(`{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-5","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Before."}]},{"id":"ig_1","type":"image_generation_call","status":"completed","result":%q},{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"After."}]}]}`, samplePNG)
	streamEvs := []string{
		`{"type":"response.created","sequence_number":1,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-5","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"output_index":0,"item_id":"msg_1","content_index":0,"delta":"Before."}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Before."}]}}`,
		`{"type":"response.output_item.added","sequence_number":5,"output_index":1,"item":{"id":"ig_1","type":"image_generation_call","status":"in_progress","result":null}}`,
		fmt.Sprintf(`{"type":"response.output_item.done","sequence_number":6,"output_index":1,"item":{"id":"ig_1","type":"image_generation_call","status":"completed","result":%q}}`, samplePNG),
		`{"type":"response.output_item.added","sequence_number":7,"output_index":2,"item":{"id":"msg_2","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":8,"output_index":2,"item_id":"msg_2","content_index":0,"delta":"After."}`,
		`{"type":"response.output_item.done","sequence_number":9,"output_index":2,"item":{"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"After."}]}}`,
		fmt.Sprintf(`{"type":"response.completed","sequence_number":10,"response":%s}`, nonStreamBody),
	}

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var evs []llm.Event
			if !stream {
				e, losses, err := openairesponses.DecodeResponseLosses([]byte(nonStreamBody))
				if err != nil || len(losses) != 0 {
					t.Fatalf("decode: err=%v losses=%+v", err, losses)
				}
				evs = roundTripMediaEvents(t, e)
			} else {
				d := openairesponses.NewStreamDecoder()
				for _, line := range streamEvs {
					e, err := d.Decode([]byte(line))
					if err != nil {
						t.Fatalf("stream decode: %v", err)
					}
					evs = append(evs, e...)
				}
				end, err := d.Close()
				if err != nil || len(d.Losses()) != 0 {
					t.Fatalf("stream close: err=%v losses=%+v", err, d.Losses())
				}
				evs = roundTripMediaEvents(t, append(evs, end...))
			}

			rBody, rSSE, rAL, rSL, _ := renderResponses(t, evs)
			if len(rAL) != 0 || (stream && len(rSL) != 0) {
				t.Errorf("responses same-format losses: %+v / %+v", rAL, rSL)
			}
			if !stream && string(rBody) != nonStreamBody {
				t.Errorf("responses same-format non-stream changed")
			}
			if stream && !strings.Contains(rSSE, `"type":"image_generation_call"`) {
				t.Errorf("responses same-format stream missing image_generation_call")
			}

			gBody, gSSE, gAL, gSL, gSize := renderGemini(t, evs)
			if len(gAL) != 0 || len(gSL) != 0 {
				t.Errorf("responses->gemini losses: %+v / %+v", gAL, gSL)
			}
			if gSize < len(samplePNG) {
				t.Errorf("gemini aggregator size %d < media bytes %d", gSize, len(samplePNG))
			}
			var gResp struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text       string `json:"text"`
							InlineData *struct {
								MimeType string `json:"mimeType"`
								Data     string `json:"data"`
							} `json:"inlineData"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
			}
			if err := json.Unmarshal(gBody, &gResp); err != nil {
				t.Fatalf("unmarshal gemini: %v", err)
			}
			parts := gResp.Candidates[0].Content.Parts
			if len(parts) != 3 || parts[0].Text != "Before." || parts[1].InlineData == nil || parts[1].InlineData.MimeType != "image/png" || parts[1].InlineData.Data != samplePNG || parts[2].Text != "After." {
				t.Errorf("gemini parts order/content wrong: %s", gBody)
			}
			beforeIdx := strings.Index(gSSE, "Before.")
			mediaIdx := strings.Index(gSSE, `"inlineData"`)
			afterIdx := strings.Index(gSSE, "After.")
			if !(beforeIdx >= 0 && mediaIdx > beforeIdx && afterIdx > mediaIdx) {
				t.Errorf("gemini SSE order wrong: before=%d media=%d after=%d\n%s", beforeIdx, mediaIdx, afterIdx, gSSE)
			}

			_, _, cAL, cSL, _ := renderChat(t, evs)
			if !hasLoss(cAL, "media.image") || !hasLoss(cSL, "media.image") {
				t.Errorf("responses->chat missing media.image loss: %+v / %+v", cAL, cSL)
			}
			_, _, mAL, mSL, _ := renderMessages(t, evs)
			if !hasLoss(mAL, "media.image") || !hasLoss(mSL, "media.image") {
				t.Errorf("responses->messages missing media.image loss: %+v / %+v", mAL, mSL)
			}
		})
	}
}

func TestChatAudioToAllFormats(t *testing.T) {
	nonStreamBody := fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"message":{"role":"assistant","content":null,"audio":{"id":"audio_abc","data":%q,"expires_at":1730000000,"transcript":"Hello."}},"finish_reason":"stop"}]}`, sampleWAV)
	streamChunks := []string{
		fmt.Sprintf(`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"role":"assistant","audio":{"id":"audio_abc","data":%q,"transcript":"Hel"}},"finish_reason":null}]}`, samplePCM),
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{"audio":{"transcript":"lo.","expires_at":1730000000}},"finish_reason":null}]}`,
		`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"gpt-4o-audio-preview","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`,
	}

	for _, stream := range []bool{false, true} {
		name := "nonstream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var evs []llm.Event
			if !stream {
				e, losses, err := openaichat.DecodeResponseLosses([]byte(nonStreamBody))
				if err != nil || len(losses) != 0 {
					t.Fatalf("decode: err=%v losses=%+v", err, losses)
				}
				evs = roundTripMediaEvents(t, e)
			} else {
				d := openaichat.NewStreamDecoder()
				for _, ch := range streamChunks {
					e, err := d.Decode([]byte(ch))
					if err != nil {
						t.Fatalf("stream decode: %v", err)
					}
					evs = append(evs, e...)
				}
				end, err := d.Close()
				if err != nil || len(d.Losses()) != 0 {
					t.Fatalf("stream close: err=%v losses=%+v", err, d.Losses())
				}
				evs = roundTripMediaEvents(t, append(evs, end...))
			}

			wantData, wantMT := sampleWAV, "audio/wav"
			if stream {
				wantData, wantMT = samplePCM, "audio/pcm;rate=24000"
			}
			gBody, gSSE, gAL, gSL, gSize := renderGemini(t, evs)
			if len(gAL) != 0 || len(gSL) != 0 {
				t.Errorf("chat->gemini losses: %+v / %+v", gAL, gSL)
			}
			if gSize < len(wantData) {
				t.Errorf("gemini aggregator size %d < %d", gSize, len(wantData))
			}
			if !strings.Contains(string(gBody), `"mimeType":"`+wantMT+`"`) || !strings.Contains(string(gBody), wantData) {
				t.Errorf("chat->gemini body missing audio: %s", gBody)
			}
			if !strings.Contains(gSSE, `"mimeType":"`+wantMT+`"`) || !strings.Contains(gSSE, wantData) {
				t.Errorf("chat->gemini SSE missing audio: %s", gSSE)
			}

			cBody, cSSE, cAL, cSL, cSize := renderChat(t, evs)
			if len(cAL) != 0 || len(cSL) != 0 {
				t.Errorf("chat same-format unexpected losses: %+v / %+v", cAL, cSL)
			}
			if cSize < len(wantData) {
				t.Errorf("chat aggregator size %d < %d", cSize, len(wantData))
			}
			if !strings.Contains(string(cBody), `"audio":{`) || !strings.Contains(string(cBody), wantData) || !strings.Contains(string(cBody), `"audio_abc"`) || !strings.Contains(string(cBody), `"Hello."`) {
				t.Errorf("chat same-format body missing audio: %s", cBody)
			}
			if !strings.Contains(cSSE, `"audio":{`) || !strings.Contains(cSSE, wantData) || !strings.Contains(cSSE, `"audio_abc"`) {
				t.Errorf("chat same-format SSE missing audio: %s", cSSE)
			}

			_, _, rAL, rSL, _ := renderResponses(t, evs)
			if !hasLoss(rAL, "media.audio") || !hasLoss(rSL, "media.audio") {
				t.Errorf("chat->responses missing media.audio loss: %+v / %+v", rAL, rSL)
			}
			_, _, mAL, mSL, _ := renderMessages(t, evs)
			if !hasLoss(mAL, "media.audio") || !hasLoss(mSL, "media.audio") {
				t.Errorf("chat->messages missing media.audio loss: %+v / %+v", mAL, mSL)
			}
		})
	}
}

func TestMediaOnlyReply(t *testing.T) {
	body := fmt.Sprintf(`{"responseId":"gm1","modelVersion":"gemini-2.5-flash-image","candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]},"finishReason":"STOP"}]}`, samplePNG)
	evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil || len(losses) != 0 {
		t.Fatalf("decode: err=%v losses=%+v", err, losses)
	}
	evs = roundTripMediaEvents(t, evs)

	rBody, rSSE, rAL, rSL, _ := renderResponses(t, evs)
	if strings.Contains(string(rBody), "image_generation_call") || strings.Contains(rSSE, "image_generation_call") {
		t.Errorf("media-only reply invented an image item: body=%s", rBody)
	}
	if !hasLoss(rAL, "media.image") || !hasLoss(rSL, "media.image") {
		t.Errorf("media-only reply missing media.image loss: %+v / %+v", rAL, rSL)
	}
	gBody, _, _, _, _ := renderGemini(t, evs)
	if !strings.Contains(string(gBody), samplePNG) {
		t.Errorf("media-only reply lost its image for a Gemini client: %s", gBody)
	}
}

func TestUnsafeAndMalformedMediaDroppedWithLoss(t *testing.T) {
	body := fmt.Sprintf(`{"responseId":"gbad","modelVersion":"gemini-2.5-flash","candidates":[{"index":0,"content":{"role":"model","parts":[
		{"text":"Keep me."},
		{"inlineData":{"mimeType":"application/pdf","data":%q}},
		{"inlineData":{"mimeType":"image/png","data":"!!!not-base64!!!"}},
		{"inlineData":{"mimeType":"image/svg+xml","data":%q}},
		{"text":" Still here."}
	]},"finishReason":"STOP"}]}`, samplePNG, sampleSVG)

	evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatalf("decode failed on bad media: %v", err)
	}
	if len(losses) != 3 {
		t.Fatalf("got %d losses (%+v), want 3", len(losses), losses)
	}
	assertNoMediaBytesInLosses(t, losses, samplePNG, sampleSVG, "!!!not-base64!!!")
	evs = roundTripMediaEvents(t, evs)
	for _, ev := range evs {
		if ev.Type == llm.EventMedia {
			t.Errorf("unexpected EventMedia emitted for unsafe/invalid parts: %+v", ev.Media)
		}
	}
	cBody, _, _, _, _ := renderChat(t, evs)
	if !strings.Contains(string(cBody), "Keep me. Still here.") {
		t.Errorf("text did not arrive whole: %s", cBody)
	}

	svgResp := fmt.Sprintf(`{"id":"r_svg","object":"response","status":"completed","model":"gpt-5","output":[{"id":"ig_svg","type":"image_generation_call","status":"completed","result":%q},{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Safe text."}]}]}`, sampleSVG)
	rEvs, rLosses, err := openairesponses.DecodeResponseLosses([]byte(svgResp))
	if err != nil {
		t.Fatalf("responses svg decode failed: %v", err)
	}
	if len(rLosses) != 1 {
		t.Fatalf("responses svg losses = %+v, want 1", rLosses)
	}
	assertNoMediaBytesInLosses(t, rLosses, sampleSVG)
	for _, ev := range rEvs {
		if ev.Type == llm.EventMedia {
			t.Errorf("responses emitted EventMedia for SVG")
		}
	}
}

func TestOutputMediaFrameAndReplyLimits(t *testing.T) {
	part22 := strings.Repeat("A", 22<<20)
	d := geminigenerate.NewStreamDecoder()
	mediaCount := 0
	for i := range 3 {
		chunk := fmt.Sprintf(`{"responseId":"gbig","modelVersion":"m","candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]}}]}`, part22)
		evs, err := d.Decode([]byte(chunk))
		if err != nil {
			t.Fatalf("chunk %d decode: %v", i, err)
		}
		for _, ev := range evs {
			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal chunk %d event: %v", i, err)
			}
			if len(b) > protocol.MaxFrame {
				t.Fatalf("chunk %d event is %d bytes > protocol.MaxFrame (%d)", i, len(b), protocol.MaxFrame)
			}
			if ev.Type == llm.EventMedia {
				mediaCount++
			}
		}
	}
	for i := 3; i < 5; i++ {
		chunk := fmt.Sprintf(`{"responseId":"gbig","modelVersion":"m","candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":%q}}]}}]}`, part22)
		evs, err := d.Decode([]byte(chunk))
		if err != nil {
			t.Fatalf("chunk %d decode: %v", i, err)
		}
		for _, ev := range evs {
			if ev.Type == llm.EventMedia {
				mediaCount++
			}
		}
	}
	if _, err := d.Decode([]byte(`{"responseId":"gbig","modelVersion":"m","candidates":[{"index":0,"finishReason":"STOP"}]}`)); err != nil {
		t.Fatalf("finish decode: %v", err)
	}
	if _, err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if mediaCount != 4 {
		t.Errorf("mediaCount = %d, want 4 (88 MiB kept, 5th at 110 MiB dropped)", mediaCount)
	}
	if len(d.Losses()) == 0 {
		t.Errorf("expected reply-limit loss for 5th part, got none")
	}

	part25 := strings.Repeat("A", (25 << 20))
	d2 := geminigenerate.NewStreamDecoder()
	evs, err := d2.Decode([]byte(fmt.Sprintf(`{"responseId":"gover","modelVersion":"m","candidates":[{"index":0,"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":%q}},{"text":"ok"}]},"finishReason":"STOP"}]}`, part25)))
	if err != nil {
		t.Fatalf("oversize part decode errored: %v", err)
	}
	for _, ev := range evs {
		if ev.Type == llm.EventMedia {
			t.Errorf("oversize part should not emit EventMedia")
		}
	}
	if len(d2.Losses()) != 1 {
		t.Errorf("oversize part losses = %+v, want 1", d2.Losses())
	}
}

func TestNextTurnReplayAcrossAllEncoders(t *testing.T) {
	geminiNextTurn := fmt.Sprintf(`{"contents":[
		{"role":"user","parts":[{"text":"Make an image and audio."}]},
		{"role":"model","parts":[{"text":"Here they are."},{"inlineData":{"mimeType":"image/png","data":%q}},{"inlineData":{"mimeType":"audio/wav","data":%q}}]},
		{"role":"user","parts":[{"text":"Now describe them."}]}
	]}`, samplePNG, sampleWAV)

	gReq, err := geminigenerate.ParseRequest("gemini-2.5-flash", false, []byte(geminiNextTurn))
	if err != nil {
		t.Fatalf("geminigenerate.ParseRequest: %v", err)
	}
	if b, _, err := geminigenerate.EncodeRequest(gReq); err != nil || !strings.Contains(string(b), samplePNG) {
		t.Errorf("gemini->gemini replay: err=%v hasPNG=%v", err, strings.Contains(string(b), samplePNG))
	}
	if b, losses, err := openairesponses.EncodeRequest(gReq); err != nil {
		t.Errorf("gemini->responses replay failed: %v", err)
	} else if strings.Contains(string(b), samplePNG) || strings.Contains(string(b), sampleWAV) || len(losses) < 2 {
		t.Errorf("gemini->responses replay should drop assistant media with losses: losses=%+v body=%s", losses, b)
	}
	if b, losses, err := openaichat.EncodeRequest(gReq); err != nil {
		t.Errorf("gemini->chat replay failed: %v", err)
	} else if strings.Contains(string(b), samplePNG) || strings.Contains(string(b), sampleWAV) || len(losses) < 2 {
		t.Errorf("gemini->chat replay should drop assistant media with losses: losses=%+v body=%s", losses, b)
	}
	if b, losses, err := anthropicmessages.EncodeRequest(gReq); err != nil {
		t.Errorf("gemini->messages replay failed: %v", err)
	} else if strings.Contains(string(b), samplePNG) || strings.Contains(string(b), sampleWAV) || len(losses) < 2 {
		t.Errorf("gemini->messages replay should drop assistant media with losses: losses=%+v body=%s", losses, b)
	}

	respNextTurn := fmt.Sprintf(`{"model":"gpt-5","input":[
		{"type":"message","role":"user","content":"Draw a dot."},
		{"id":"ig_g1_0","type":"image_generation_call","status":"completed","result":%q},
		{"type":"message","role":"user","content":"Make it bigger."}
	]}`, samplePNG)
	rReq, err := openairesponses.ParseRequest([]byte(respNextTurn))
	if err != nil {
		t.Fatalf("openairesponses.ParseRequest: %v", err)
	}
	if b, _, err := openairesponses.EncodeRequest(rReq); err != nil || !strings.Contains(string(b), `"image_generation_call"`) {
		t.Errorf("responses->responses replay: err=%v body=%s", err, b)
	}
	for name, enc := range map[string]func(llm.Request) ([]byte, []llm.Loss, error){
		"gemini":   func(r llm.Request) ([]byte, []llm.Loss, error) { return geminigenerate.EncodeRequest(r) },
		"chat":     func(r llm.Request) ([]byte, []llm.Loss, error) { return openaichat.EncodeRequest(r) },
		"messages": func(r llm.Request) ([]byte, []llm.Loss, error) { return anthropicmessages.EncodeRequest(r) },
	} {
		if b, losses, err := enc(rReq); err != nil {
			t.Errorf("responses->%s replay failed: %v", name, err)
		} else if strings.Contains(string(b), samplePNG) || len(losses) == 0 {
			t.Errorf("responses->%s replay should drop image_generation_call with loss: losses=%+v", name, losses)
		}
	}

	chatNextTurn := `{"model":"gpt-4o-audio-preview","messages":[
		{"role":"user","content":"Say hi."},
		{"role":"assistant","audio":{"id":"audio_abc"}},
		{"role":"user","content":"Say it louder."}
	]}`
	cReq, err := openaichat.ParseRequest([]byte(chatNextTurn))
	if err != nil {
		t.Fatalf("openaichat.ParseRequest: %v", err)
	}
	if b, _, err := openaichat.EncodeRequest(cReq); err != nil || !strings.Contains(string(b), `"audio_abc"`) {
		t.Errorf("chat->chat replay: err=%v body=%s", err, b)
	}
	for name, enc := range map[string]func(llm.Request) ([]byte, []llm.Loss, error){
		"gemini":    func(r llm.Request) ([]byte, []llm.Loss, error) { return geminigenerate.EncodeRequest(r) },
		"responses": func(r llm.Request) ([]byte, []llm.Loss, error) { return openairesponses.EncodeRequest(r) },
		"messages":  func(r llm.Request) ([]byte, []llm.Loss, error) { return anthropicmessages.EncodeRequest(r) },
	} {
		if b, losses, err := enc(cReq); err != nil {
			t.Errorf("chat->%s replay failed: %v", name, err)
		} else if strings.Contains(string(b), `"audio_abc"`) || len(losses) == 0 {
			t.Errorf("chat->%s replay should drop audio.id with loss: losses=%+v body=%s", name, losses, b)
		}
	}
}
