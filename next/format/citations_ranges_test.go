package format_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

const groundingAAAA = `"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/","title":"A"}}],` +
	`"groundingSupports":[{"segment":{"startIndex":0,"endIndex":4,"text":"AAAA"},"groundingChunkIndices":[0]}]}`

func geminiStream(t *testing.T, chunks ...string) []llm.Event {
	t.Helper()
	d := geminigenerate.NewStreamDecoder()
	var all []llm.Event
	for _, c := range chunks {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, evs...)
	}
	evs, err := d.Close()
	if err != nil {
		t.Fatal(err)
	}
	return append(all, evs...)
}

func citationsOf(evs []llm.Event) []llm.Citation {
	var out []llm.Citation
	for _, ev := range evs {
		out = append(out, ev.Citations...)
	}
	return out
}

// A part index counts within one chunk, so a later chunk's part 0 must not stand for the text.
func TestGeminiStreamRangeAcrossChunks(t *testing.T) {
	cits := citationsOf(geminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"hm","thought":true},{"text":"AAAA"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"BBBB"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"CCCC"}]},"finishReason":"STOP",`+groundingAAAA+`}]}`))
	if len(cits) != 1 || cits[0].StartIndex == nil || *cits[0].StartIndex != 0 || *cits[0].EndIndex != 4 {
		t.Fatalf("citations = %+v, want one over 0-4", cits)
	}
}

func TestGeminiStreamRepeatedMetadataCitesOnce(t *testing.T) {
	cits := citationsOf(geminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"AAAA"}]},`+groundingAAAA+`}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"BB"}]},"finishReason":"STOP",`+groundingAAAA+`}]}`))
	if len(cits) != 1 {
		t.Fatalf("citations = %+v, want one", cits)
	}
}

// Metadata after the text item closed must not attach to a later text item.
func TestGeminiLateMetadataKeepsItsSourceOnly(t *testing.T) {
	evs := geminiStream(t,
		`{"candidates":[{"content":{"parts":[{"text":"AAAA"},{"functionCall":{"name":"f","args":{}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[]},`+groundingAAAA+`}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"later"}]},"finishReason":"STOP"}]}`)
	if cits := citationsOf(evs); len(cits) != 0 {
		t.Errorf("citations = %+v, want none", cits)
	}
	if last := evs[len(evs)-1]; len(last.Sources) != 1 {
		t.Errorf("sources = %+v, want the one source", last.Sources)
	}
}

// Upstream offsets count the leading think tag that the decoder strips from the text.
func TestChatStreamRangeSkipsThinkTag(t *testing.T) {
	d := openaichat.NewStreamDecoder()
	var all []llm.Event
	for _, c := range []string{
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"<think>x</think>hello world, hello"}}]}`,
		`{"id":"c","model":"m","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://a.example/","title":"A","start_index":16,"end_index":21}}]},"finish_reason":"stop"}]}`,
	} {
		evs, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, evs...)
	}
	evs, err := d.Close()
	if err != nil {
		t.Fatal(err)
	}
	cits := citationsOf(append(all, evs...))
	if len(cits) != 1 || cits[0].StartIndex == nil || *cits[0].StartIndex != 0 || *cits[0].EndIndex != 5 {
		t.Fatalf("citations = %+v, want one over 0-5", cits)
	}
}

// OpenAI's references list start_index and end_index as required on a URL citation.
func TestCitationWithNoRangeCoversItsItem(t *testing.T) {
	events := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "m", Format: llm.FormatGeminiGenerate}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "héllo"},
		{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://a.example/", Title: "A"}}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	chat := openaichat.NewAggregator()
	responses := openairesponses.NewAggregator(llm.Request{Model: "m"})
	for _, ev := range events {
		chat.Add(ev)
		responses.Add(ev)
	}
	for name, resp := range map[string]func() ([]byte, error){"chat": chat.Response, "responses": responses.Response} {
		body, err := resp()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), `"start_index":0`) || !strings.Contains(string(body), `"end_index":5`) {
			t.Errorf("%s: want start_index 0 and end_index 5 in %s", name, body)
		}
	}
}

// A Chat upstream's reply to a Chat client keeps the bytes it has without this feature.
func TestChatSameFormatReplyUnchanged(t *testing.T) {
	body := `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-5-search-api","choices":[{"index":0,"finish_reason":"stop",` +
		`"message":{"role":"assistant","content":"hello world","annotations":[{"type":"url_citation","url_citation":{"url":"https://a.example/","title":"A","start_index":0,"end_index":5}}]}}]}`
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
	var got struct {
		Choices []struct {
			Message map[string]json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Choices[0].Message["annotations"]; ok {
		t.Errorf("same-format reply gained annotations: %s", out)
	}
}
