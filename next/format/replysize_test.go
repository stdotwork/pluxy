package format_test

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// A reply whose citations and sources are larger than a reply may carry keeps those that fit and
// reports the rest as lost, on every decoder and on both the streamed and the whole path.

const (
	manyCites = 40
	citeTitle = 256 << 10 // 40 of them are 10 MiB, over llm.MaxCitationBytes
)

func bigTitle() string { return strings.Repeat("t", citeTitle) }

// citedBytes is what a reply's events hold in citations, and in sources.
func citedBytes(evs []llm.Event) (cites, sources int) {
	for _, ev := range evs {
		for _, c := range ev.Citations {
			cites += len(c.URL) + len(c.Title)
		}
		for _, s := range ev.Sources {
			sources += len(s.URL) + len(s.Title)
		}
	}
	return cites, sources
}

func checkCitationsCapped(t *testing.T, evs []llm.Event, losses []llm.Loss) {
	t.Helper()
	cites, sources := citedBytes(evs)
	if cites+sources == 0 {
		t.Fatal("no citation or source kept: the cap must keep what fits")
	}
	if cites > llm.MaxCitationBytes || sources > llm.MaxCitationBytes {
		t.Errorf("the reply keeps %d MiB of citations and %d MiB of sources, want at most %d MiB of each", cites>>20, sources>>20, llm.MaxCitationBytes>>20)
	}
	for _, ev := range evs {
		if err := protocol.WriteFrame(io.Discard, protocol.Reply{Event: ev}); err != nil {
			t.Errorf("%s event: %v", ev.Type, err)
		}
	}
	if !hasLoss(losses, "citations") {
		t.Errorf("losses = %+v, want one for the citations left out", losses)
	}
}

func decodeStreamChunks(t *testing.T, d interface {
	Decode([]byte) ([]llm.Event, error)
	Close() ([]llm.Event, error)
}, chunks []string) []llm.Event {
	t.Helper()
	var evs []llm.Event
	for _, c := range chunks {
		e, err := d.Decode([]byte(c))
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		evs = append(evs, e...)
	}
	return evs
}

func TestCitationsAndSourcesAreCappedPerReply(t *testing.T) {
	title := bigTitle()
	t.Run("messages", func(t *testing.T) {
		var cites, results []string
		for i := range manyCites {
			cites = append(cites, fmt.Sprintf(`{"type":"web_search_result_location","url":"https://a.example/%d","title":%q}`, i, title))
			results = append(results, fmt.Sprintf(`{"type":"web_search_result","url":"https://s.example/%d","title":%q}`, i, title))
		}
		body := fmt.Sprintf(`{"id":"m","type":"message","role":"assistant","model":"claude","content":[
			{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[%s]},
			{"type":"text","text":"Alpha beta.","citations":[%s]}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			strings.Join(results, ","), strings.Join(cites, ","))
		evs, losses, err := anthropicmessages.DecodeResponseLosses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		checkCitationsCapped(t, evs, losses)
	})
	t.Run("messages stream", func(t *testing.T) {
		d := anthropicmessages.NewStreamDecoder()
		chunks := []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"claude","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Alpha beta."}}`,
		}
		for i := range manyCites {
			chunks = append(chunks, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://a.example/%d","title":%q}}}`, i, title))
		}
		chunks = append(chunks, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`, `{"type":"message_stop"}`)
		evs := decodeStreamChunks(t, d, chunks)
		checkCitationsCapped(t, evs, d.Losses())
	})
	t.Run("chat", func(t *testing.T) {
		var anns []string
		for i := range manyCites {
			anns = append(anns, fmt.Sprintf(`{"type":"url_citation","url_citation":{"url":"https://a.example/%d","title":%q,"start_index":0,"end_index":5}}`, i, title))
		}
		body := fmt.Sprintf(`{"id":"c","object":"chat.completion","created":1,"model":"gpt","choices":[{"index":0,"message":{"role":"assistant","content":"Alpha beta.","annotations":[%s]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, strings.Join(anns, ","))
		evs, losses, err := openaichat.DecodeResponseLosses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		checkCitationsCapped(t, evs, losses)
	})
	t.Run("chat stream", func(t *testing.T) {
		d := openaichat.NewStreamDecoder()
		chunks := []string{`{"id":"c","object":"chat.completion.chunk","created":1,"model":"gpt","choices":[{"index":0,"delta":{"role":"assistant","content":"Alpha beta."}}]}`}
		for i := range manyCites {
			chunks = append(chunks, fmt.Sprintf(`{"id":"c","object":"chat.completion.chunk","created":1,"model":"gpt","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://a.example/%d","title":%q,"start_index":0,"end_index":5}}]}}]}`, i, title))
		}
		chunks = append(chunks, `{"id":"c","object":"chat.completion.chunk","created":1,"model":"gpt","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
		evs := decodeStreamChunks(t, d, chunks)
		checkCitationsCapped(t, evs, d.Losses())
	})
	t.Run("responses", func(t *testing.T) {
		var anns, srcs []string
		for i := range manyCites {
			anns = append(anns, fmt.Sprintf(`{"type":"url_citation","url":"https://a.example/%d","title":%q,"start_index":0,"end_index":5}`, i, title))
			srcs = append(srcs, fmt.Sprintf(`{"type":"url","url":"https://s.example/%d","title":%q}`, i, title))
		}
		body := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"gpt","output":[
			{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","sources":[%s]}},
			{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"Alpha beta.","annotations":[%s]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			strings.Join(srcs, ","), strings.Join(anns, ","))
		evs, losses, err := openairesponses.DecodeResponseLosses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		checkCitationsCapped(t, evs, losses)
	})
	t.Run("responses stream", func(t *testing.T) {
		d := openairesponses.NewStreamDecoder()
		chunks := []string{
			`{"type":"response.created","response":{"id":"r","model":"gpt","status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Alpha beta."}`,
		}
		for i := range manyCites {
			chunks = append(chunks, fmt.Sprintf(`{"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation_index":%d,"annotation":{"type":"url_citation","url":"https://a.example/%d","title":%q,"start_index":0,"end_index":5}}`, i, i, title))
		}
		chunks = append(chunks,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"Alpha beta.","annotations":[]}]}}`,
			`{"type":"response.completed","response":{"id":"r","model":"gpt","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
		evs := decodeStreamChunks(t, d, chunks)
		checkCitationsCapped(t, evs, d.Losses())
	})
	t.Run("gemini", func(t *testing.T) {
		var chunks, supports []string
		for i := range manyCites {
			chunks = append(chunks, fmt.Sprintf(`{"web":{"uri":"https://a.example/%d","title":%q}}`, i, title))
			supports = append(supports, fmt.Sprintf(`{"segment":{"startIndex":0,"endIndex":5,"text":"Alpha"},"groundingChunkIndices":[%d]}`, i))
		}
		body := fmt.Sprintf(`{"responseId":"g","modelVersion":"gemini","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Alpha beta."}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[%s],"groundingSupports":[%s]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
			strings.Join(chunks, ","), strings.Join(supports, ","))
		evs, losses, err := geminigenerate.DecodeResponseLosses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		checkCitationsCapped(t, evs, losses)
	})
}

// A same-format reply carries its code runs and sources in the body it passes on as Raw, so the
// finish event must not add them to a frame the body nearly fills.
func TestFinishFitsOneFrameBesideItsRawBody(t *testing.T) {
	check := func(t *testing.T, evs []llm.Event, losses []llm.Loss) {
		t.Helper()
		last := evs[len(evs)-1]
		if last.Type != llm.EventFinish {
			t.Fatalf("last event is %s", last.Type)
		}
		if len(last.Raw) < 50<<20 {
			t.Fatalf("finish Raw is %d MiB: the test needs a reply near the limit", len(last.Raw)>>20)
		}
		if err := protocol.WriteFrame(io.Discard, protocol.Reply{Event: last}); err != nil {
			t.Errorf("finish frame: %v", err)
		}
		if !hasLoss(losses, "finish") {
			t.Errorf("losses = %+v, want one for what the finish event left out", losses)
		}
	}
	t.Run("responses runs", func(t *testing.T) {
		// Two runs of 3.9 and 58 MiB: the first fits the runs' own cap, and with the body it is too much.
		body := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"gpt","output":[
			{"type":"code_interpreter_call","id":"ci_1","status":"completed","code":"print(1)","container_id":"c","outputs":[{"type":"logs","logs":%q}]},
			{"type":"code_interpreter_call","id":"ci_2","status":"completed","code":"print(2)","container_id":"c","outputs":[{"type":"logs","logs":%q}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
			strings.Repeat("a", 3900<<10), strings.Repeat("b", 58<<20))
		evs, losses, err := openairesponses.DecodeResponseLosses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		check(t, evs, losses)
	})
	t.Run("messages sources", func(t *testing.T) {
		var results []string
		for i := range 15 {
			results = append(results, fmt.Sprintf(`{"type":"web_search_result","url":"https://s.example/%d","title":%q}`, i, strings.Repeat("t", 256<<10)))
		}
		body := fmt.Sprintf(`{"id":"m","type":"message","role":"assistant","model":"claude","content":[
			{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[%s]},
			{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
			strings.Join(results, ","), strings.Repeat("x", 59<<20))
		evs, losses, err := anthropicmessages.DecodeResponseLosses([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		check(t, evs, losses)
	})
}

// A Gemini reply of the client's own format holds its runs and sources in the chunks it keeps, so
// the aggregator does not count them again. The event here is built by hand, with its text only in
// Raw; the decoder also puts the text in Delta, which counts twice (a separate row), so this does
// not show that a plain reply of 33 to 64 MiB passes.
func TestGeminiAggregatorDoesNotCountANativeRepliesRunsAgain(t *testing.T) {
	a := geminigenerate.NewAggregator()
	raw := json.RawMessage(`{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", 62<<20) + `"}]}}]}`)
	a.Add(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "g", Model: "gemini", Format: llm.FormatGeminiGenerate}})
	a.Add(llm.Event{Type: llm.EventNative, Raw: raw})
	a.Add(llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop},
		CodeExecutions: llm.CodeExecutions{{Code: strings.Repeat("c", 3<<20), Language: "python"}}})
	if got := a.Size(); got > llm.MaxNonStreamedReplyBytes {
		t.Errorf("Size = %d MiB for a %d MiB reply that holds its runs in its chunks: it would be refused as too large", got>>20, len(raw)>>20)
	}
}

// A Gemini upstream that streams to a client that asked for the whole reply: the reply is built
// again from the chunks, and keeps its supports.
func TestSameFormatGeminiStreamKeepsItsGroundingSupports(t *testing.T) {
	d := geminigenerate.NewStreamDecoder()
	chunks := []string{
		`{"responseId":"g","modelVersion":"gemini","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Alpha beta. "}]}}]}`,
		`{"responseId":"g","modelVersion":"gemini","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Gamma delta."}]}}]}`,
		`{"responseId":"g","modelVersion":"gemini","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP","groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://a.example/1","title":"A"}},{"web":{"uri":"https://a.example/2","title":"B"}},{"web":{"uri":"https://a.example/3","title":"C"}}],"groundingSupports":[{"segment":{"startIndex":0,"endIndex":5,"text":"Alpha"},"groundingChunkIndices":[0]},{"segment":{"startIndex":6,"endIndex":10,"text":"beta"},"groundingChunkIndices":[1]},{"segment":{"startIndex":12,"endIndex":17,"text":"Gamma"},"groundingChunkIndices":[2]}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`,
	}
	evs := decodeStreamChunks(t, d, chunks)
	end, err := d.Close()
	if err != nil {
		t.Fatal(err)
	}
	a := geminigenerate.NewAggregator()
	for _, ev := range append(evs, end...) {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Candidates []struct {
			GroundingMetadata struct {
				GroundingChunks   []json.RawMessage `json:"groundingChunks"`
				GroundingSupports []json.RawMessage `json:"groundingSupports"`
			} `json:"groundingMetadata"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Candidates) == 0 {
		t.Fatalf("response: %v: %s", err, out)
	}
	gm := got.Candidates[0].GroundingMetadata
	if len(gm.GroundingChunks) != 3 || len(gm.GroundingSupports) != 3 {
		t.Errorf("chunks %d, supports %d, want 3 and 3: %s", len(gm.GroundingChunks), len(gm.GroundingSupports), out)
	}
}

// A message whose citation covers it all is written twice over, as text and as the text a support
// repeats, and that growth has to be seen while images can still be left out.
func TestCitedTextIsCountedBeforeImagesAreLeftOut(t *testing.T) {
	image := strings.Repeat("QUJD", (16515072)/4) // 15.75 MiB of valid base64, shared by every part
	text := strings.Repeat("a", 700<<10)
	end := len([]rune(text))
	zero := 0
	evs := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "gpt", Format: llm.FormatOpenAIResponses}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: text},
		{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://a.example/1", Title: "A", StartIndex: &zero, EndIndex: &end}}},
	}
	for range 4 {
		evs = append(evs, llm.Event{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: image}})
	}
	evs = append(evs, llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}})
	a := geminigenerate.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Size(); got > llm.MaxNonStreamedReplyBytes {
		t.Errorf("Size after the response = %d MiB, over the %d MiB limit the server then fails the reply at", got>>20, llm.MaxNonStreamedReplyBytes>>20)
	}
	if n := strings.Count(string(out), `"inlineData"`); n < 1 {
		t.Errorf("%d images kept, want the ones that fit", n)
	}
	if !strings.Contains(string(out), text[:100]) {
		t.Error("the reply lost its text")
	}
}

// Sources a Chat client never sees do not use up the room of the citations it does.
func TestSourcesDoNotUseUpTheCitationBudget(t *testing.T) {
	title := strings.Repeat("t", 20<<10)
	var srcs, anns []string
	for i := range 200 {
		srcs = append(srcs, fmt.Sprintf(`{"type":"url","url":"https://s.example/%d","title":%q}`, i, title))
		anns = append(anns, fmt.Sprintf(`{"type":"url_citation","url":"https://a.example/%d","title":%q,"start_index":0,"end_index":5}`, i, title))
	}
	body := fmt.Sprintf(`{"id":"r","object":"response","status":"completed","model":"gpt","output":[
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","sources":[%s]}},
		{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"Alpha beta.","annotations":[%s]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		strings.Join(srcs, ","), strings.Join(anns, ","))
	evs, losses, err := openairesponses.DecodeResponseLosses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var cites, sources int
	for _, ev := range evs {
		cites += len(ev.Citations)
		sources += len(ev.Sources)
	}
	if cites != 200 || sources != 200 {
		t.Errorf("kept %d of 200 citations and %d of 200 sources, want all of both: each has 4 MiB to itself", cites, sources)
	}
	if hasLoss(losses, "citations") {
		t.Errorf("losses = %+v, want none: both fit", losses)
	}
}

// Each support repeats its cited text, so a long message with many citations is longer than the
// text and the citations it came as.
func TestGeminiSegmentTextIsCountedAndBounded(t *testing.T) {
	text := strings.Repeat("a", 1<<20)
	var cites []string
	for i := range 70 {
		cites = append(cites, fmt.Sprintf(`{"type":"web_search_result_location","url":"https://a.example/%d","title":"t"}`, i))
	}
	body := fmt.Sprintf(`{"id":"m","type":"message","role":"assistant","model":"claude","content":[
		{"type":"text","text":%q,"citations":[%s]}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, text, strings.Join(cites, ","))
	evs, err := anthropicmessages.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	a := geminigenerate.NewAggregator()
	for _, ev := range evs {
		a.Add(ev)
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	if a.Size() < len(out)-len(out)/20 { // Size is about the bytes, before JSON escaping
		t.Errorf("Size = %d for a response of %d bytes: the check against the reply limit does not see it", a.Size(), len(out))
	}
	if len(out) > 4<<20 {
		t.Errorf("response is %d MiB for a 1 MiB message", len(out)>>20)
	}
	if !hasLoss(a.Losses(), "groundingMetadata.groundingSupports.segment.text") {
		t.Errorf("losses = %+v, want one for the cited text left out", a.Losses())
	}
}

// Images that together pass the limit for a reply sent whole are left out, last first, with a
// loss, and the text arrives.
func TestImagesOverTheNonStreamedLimitAreLeftOut(t *testing.T) {
	image := strings.Repeat("QUJD", (15<<20)/4) // 15 MiB of valid base64, shared by every part
	a := geminigenerate.NewAggregator()
	evs := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "gpt", Format: llm.FormatOpenAIResponses}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "the text"},
		{Type: llm.EventItemEnd, Index: 0},
	}
	for range 5 {
		evs = append(evs, llm.Event{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: image}})
	}
	evs = append(evs, llm.Event{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}})
	for _, ev := range evs {
		a.Add(ev)
		if got := a.Size(); got > llm.MaxNonStreamedReplyBytes {
			t.Fatalf("after %s the reply holds %d MiB, over the %d MiB limit the server fails it at", ev.Type, got>>20, llm.MaxNonStreamedReplyBytes>>20)
		}
	}
	out, err := a.Response()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "the text") {
		t.Error("the reply lost its text")
	}
	if n := strings.Count(string(out), `"inlineData"`); n < 1 || n >= 5 {
		t.Errorf("%d of 5 images kept, want some but not all", n)
	}
	if !hasLoss(a.Losses(), "media.image") {
		t.Errorf("losses = %+v, want one for the images left out", a.Losses())
	}
	if got := a.Size(); got > llm.MaxNonStreamedReplyBytes {
		t.Errorf("Size after the response = %d MiB, over the limit", got>>20)
	}
}
