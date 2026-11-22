package format_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// An unlabelled image whose header is padding then four other bytes must not crash the decoder.
func TestImageSniffShortHeadAfterPadding(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat(" ", 14) + "abcd"))
	body := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"m","output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":%q}]}`, data)
	if _, _, err := openairesponses.DecodeResponseLosses([]byte(body)); err != nil {
		t.Fatal(err)
	}
}

// An unlabelled SVG is still refused.
func TestImageSniffRefusesSVG(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat(" ", 14) + "<svg"))
	body := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"m","output":[{"id":"ig_1","type":"image_generation_call","status":"completed","result":%q}]}`, data)
	evs, losses, err := openairesponses.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Type == llm.EventMedia {
			t.Fatalf("SVG carried as %q", ev.Media.MediaType)
		}
	}
	if len(losses) != 1 {
		t.Fatalf("losses = %+v, want one", losses)
	}
}

// A streamed piece of PCM has no header, and silence (0xFF 0xFF) looks like an MP3 frame.
func TestChatStreamAudioIsNotSniffed(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	d := openaichat.NewStreamDecoder()
	evs, err := d.Decode(fmt.Appendf(nil, `{"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","audio":{"id":"a","data":%q}}}]}`, data))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, ev := range evs {
		if ev.Type == llm.EventMedia {
			got = ev.Media.MediaType
		}
	}
	if got != "audio/pcm;rate=24000" {
		t.Errorf("media type = %q", got)
	}
}

// Audio arriving between pieces of text must not split the text item.
func TestChatStreamAudioKeepsTextItem(t *testing.T) {
	d := openaichat.NewStreamDecoder()
	var evs []llm.Event
	for _, c := range []string{
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"one "}}]}`,
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"audio":{"id":"a","data":"AQID"}}}]}`,
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"content":"two"},"finish_reason":"stop"}]}`,
	} {
		e, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e...)
	}
	starts := 0
	for _, ev := range evs {
		if ev.Type == llm.EventItemStart {
			starts++
		}
	}
	if starts != 1 {
		t.Errorf("text opened %d items, want 1", starts)
	}
}

// Google's offsets count within a part, so once media split the text they cannot name a range.
func TestGeminiCitationAfterMediaHasNoRange(t *testing.T) {
	body := `{"responseId":"g","candidates":[{"content":{"role":"model","parts":[{"text":"AAAA"},{"inlineData":{"mimeType":"image/png","data":"AQID"}},{"text":"BBBB"}]},"finishReason":"STOP",` + groundingAAAA + `}]}`
	evs, err := geminigenerate.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	cits := citationsOf(evs)
	if len(cits) != 1 || cits[0].StartIndex != nil {
		t.Fatalf("citations = %+v, want one with no range", cits)
	}
}

// A Responses item needs an ID only OpenAI issues, which the client would send back.
func TestResponsesClientGetsNoInventedImageItem(t *testing.T) {
	evs := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "g", Model: "m", Format: llm.FormatGeminiGenerate}},
		{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: "AQID"}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	a := openairesponses.NewAggregator(llm.Request{Model: "m"})
	for _, ev := range evs {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Output) != 0 {
		t.Errorf("output = %s, want none", got.Output)
	}
}

// A Chat upstream's reply to a Chat client keeps the bytes it has without this feature.
func TestChatSameFormatAudioReplyUnchanged(t *testing.T) {
	body := `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,"audio":{"id":"a","data":"AQID","expires_at":1,"transcript":"hi"}}}]}`
	evs, _, err := openaichat.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	a := openaichat.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`
	if string(out) != want {
		t.Errorf("reply = %s\nwant %s", out, want)
	}
}
