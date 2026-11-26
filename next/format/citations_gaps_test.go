package format_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func hasLossPath(losses []llm.Loss, path string) bool {
	return slices.ContainsFunc(losses, func(l llm.Loss) bool { return l.Path == path })
}

func lossPaths(losses []llm.Loss) []string {
	var out []string
	for _, l := range losses {
		out = append(out, l.Path)
	}
	return out
}

// citedReply is a reply from a Messages upstream: one message item, with a citation on "Alpha",
// and two web search sources, one of them the cited one.
func citedReply(cited, uncited string) []llm.Event {
	s, e := 0, 5
	return []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "m1", Model: "m", Format: llm.FormatAnthropicMessages}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "Alpha beta."},
		{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: cited, Title: "A", StartIndex: &s, EndIndex: &e}}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Sources: llm.Sources{{URL: cited, Title: "A"}, {URL: uncited, Title: "B"}}},
	}
}

// A Chat client is given the citations of a JSON reply as annotations; what it cannot be given is
// reported, and what it is given is not.
func TestChatClientReportsWhatItIsNotGivenJSON(t *testing.T) {
	evs := roundTripEvents(t, citedReply("https://a.example/", "https://b.example/"))
	got, body := chatReply(t, evs)
	if len(got.Choices) != 1 || !strings.Contains(body, `"url":"https://a.example/"`) || !strings.Contains(body, `"annotations"`) {
		t.Fatalf("the citation did not reach the client: %s", body)
	}
	agg := openaichat.NewAggregator()
	for _, ev := range evs {
		agg.Add(ev)
	}
	if _, err := agg.Response(); err != nil {
		t.Fatal(err)
	}
	losses := agg.Losses()
	if !hasLossPath(losses, "sources") {
		t.Errorf("the uncited source is not reported lost: %v", lossPaths(losses))
	}
	if hasLossPath(losses, "content.citations") {
		t.Errorf("the delivered citation is reported lost: %v", lossPaths(losses))
	}

	// With every source cited there is nothing left to report.
	agg = openaichat.NewAggregator()
	for _, ev := range roundTripEvents(t, citedReply("https://a.example/", "https://a.example/")) {
		agg.Add(ev)
	}
	if _, err := agg.Response(); err != nil {
		t.Fatal(err)
	}
	if len(agg.Losses()) != 0 {
		t.Errorf("losses = %v, want none: every source is cited", lossPaths(agg.Losses()))
	}
}

// A Chat stream from another format has no annotations, so what it left out is reported.
func TestChatClientReportsWhatItIsNotGivenStream(t *testing.T) {
	var sb strings.Builder
	w := openaichat.NewStreamWriter(&sb, true)
	for _, ev := range roundTripEvents(t, citedReply("https://a.example/", "https://b.example/")) {
		if err := w.Write(ev); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(sb.String(), "annotations") {
		t.Fatalf("a cross-format Chat stream carries annotations now; this test needs to change:\n%s", sb.String())
	}
	losses := w.Losses()
	for _, p := range []string{"content.citations", "sources"} {
		if !hasLossPath(losses, p) {
			t.Errorf("the stream does not report %q lost: %v", p, lossPaths(losses))
		}
	}
}

func responsesDoneNone(t *testing.T, chunks ...string) []llm.Event {
	t.Helper()
	return decodeResponsesStream(t, chunks)
}

const respCreated = `{"type":"response.created","response":{"id":"r1","model":"m","status":"in_progress","output":[]}}`
const respAdded = `{"type":"response.output_item.added","output_index":0,"item":{"id":"m1","type":"message","status":"in_progress","role":"assistant","content":[]}}`

func respDelta(part int, text string) string {
	return `{"type":"response.output_text.delta","output_index":0,"content_index":` + string(rune('0'+part)) + `,"delta":"` + text + `"}`
}

func respAnn(part int, start, end int) string {
	return `{"type":"response.output_text.annotation.added","output_index":0,"content_index":` + string(rune('0'+part)) +
		`,"annotation_index":0,"annotation":{"type":"url_citation","url":"https://a.example/","title":"A","start_index":` +
		string(rune('0'+start)) + `,"end_index":` + string(rune('0'+end)) + `}}`
}

// A stream with no output_item.done still ends with the citations its annotation events carried.
func TestResponsesStreamWithoutItemDoneKeepsCitations(t *testing.T) {
	want := map[string][]string{"Alpha beta. ": {"https://a.example/ Alpha"}}
	t.Run("annotation.added", func(t *testing.T) {
		evs := responsesDoneNone(t, respCreated, respAdded, respDelta(0, "Alpha beta. "), respAnn(0, 0, 5),
			`{"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","output":[]}}`)
		wantCited(t, citedTexts(t, evs), want)
	})
	t.Run("completed output", func(t *testing.T) {
		evs := responsesDoneNone(t, respCreated, respAdded, respDelta(0, "Alpha beta. "),
			`{"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","output":[{"id":"m1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Alpha beta. ","annotations":[{"type":"url_citation","url":"https://a.example/","title":"A","start_index":0,"end_index":5}]}]}]}}`)
		wantCited(t, citedTexts(t, evs), want)
	})
}

// An annotation event says which content part it is on: its offsets count within that part.
func TestResponsesAnnotationEventKeepsItsContentIndex(t *testing.T) {
	evs := decodeResponsesStream(t, []string{respCreated, respAdded,
		respDelta(0, "Alpha beta. "), respDelta(1, "Gamma delta."), respAnn(1, 0, 5),
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Alpha beta. ","annotations":[]},{"type":"output_text","text":"Gamma delta.","annotations":[]}]}}`,
		`{"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","output":[]}}`})
	wantCited(t, citedTexts(t, evs), map[string][]string{"Alpha beta. Gamma delta.": {"https://a.example/ Gamma"}})
}

func chatAnnChunk(delta string) string {
	return `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + delta + `}]}`
}

// Annotations that arrive with no text open have no citation to join. A client of another format
// is told so; a Chat client is given them as they came, so it loses nothing.
func TestChatAnnotationsOutsideOpenTextAreReported(t *testing.T) {
	ann := `{"annotations":[{"type":"url_citation","url_citation":{"url":"https://a.example/","title":"A","start_index":0,"end_index":5}}]}`
	text := `{"content":"Alpha beta."}`
	for name, chunks := range map[string][]string{
		"before the first text": {chatAnnChunk(`{"role":"assistant","content":""}`), chatAnnChunk(ann), chatAnnChunk(text),
			`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`},
		"after the finish": {chatAnnChunk(`{"role":"assistant","content":""}`), chatAnnChunk(text),
			`{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, chatAnnChunk(ann)},
	} {
		t.Run(name, func(t *testing.T) {
			dec := openaichat.NewStreamDecoder()
			for _, c := range chunks {
				if _, err := dec.Decode([]byte(c)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := dec.Close(); err != nil {
				t.Fatal(err)
			}
			if got := llm.ReplyLosses(dec.Losses(), false); !hasLossPath(got, llm.LossPathCitations) {
				t.Errorf("a client of another format is not told: %v", lossPaths(got))
			}
			if got := llm.ReplyLosses(dec.Losses(), true); len(got) != 0 {
				t.Errorf("a Chat client, given the body as it came, is told of %v", lossPaths(got))
			}
		})
	}
	// With the text open, the same annotation is a citation, and nothing is lost.
	dec := openaichat.NewStreamDecoder()
	for _, c := range []string{chatAnnChunk(`{"role":"assistant","content":"Alpha beta."}`), chatAnnChunk(ann)} {
		if _, err := dec.Decode([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if len(dec.Losses()) != 0 {
		t.Errorf("losses = %v, want none", lossPaths(dec.Losses()))
	}
}

// A Responses annotation that a Gemini client is given as a citation is not reported lost; one
// that has no citation to become is, and a Messages client, which is given no citation, is told.
func TestDeliveredResponsesAnnotationsAreNotReportedLost(t *testing.T) {
	annotated := func(ann string) []string {
		return []string{respCreated, respAdded, respDelta(0, "Alpha beta. "),
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Alpha beta. ","annotations":[` + ann + `]}]}}`,
			`{"type":"response.completed","response":{"id":"r1","model":"m","status":"completed","output":[{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Alpha beta. ","annotations":[` + ann + `]}]}]}}`}
	}
	const (
		url  = `{"type":"url_citation","url":"https://a.example/","title":"A","start_index":0,"end_index":5}`
		file = `{"type":"file_citation","file_id":"f1","index":0}`
	)
	const path = "output[0].content[0].annotations"
	render := func(t *testing.T, evs []llm.Event) map[string][]llm.Loss {
		_, gAgg := renderGeminiAgg(t, evs)
		_, gStream := renderGeminiStream(t, evs)
		_, mAgg := renderMessagesAgg(t, evs)
		_, mStream := renderMessagesStream(t, evs)
		return map[string][]llm.Loss{"gemini json": gAgg, "gemini stream": gStream, "messages json": mAgg, "messages stream": mStream}
	}
	for name, losses := range render(t, decodeResponsesStream(t, annotated(url))) {
		if strings.HasPrefix(name, "messages") {
			if !hasLossPath(losses, "content.citations") {
				t.Errorf("%s: the citation it is not given is not reported lost: %v", name, lossPaths(losses))
			}
			continue
		}
		if hasLossPath(losses, path) {
			t.Errorf("%s: the delivered citation is reported lost: %v", name, lossPaths(losses))
		}
	}
	for name, losses := range render(t, decodeResponsesStream(t, annotated(file))) {
		if !hasLossPath(losses, path) {
			t.Errorf("%s: an annotation that is no citation is not reported lost: %v", name, lossPaths(losses))
		}
	}
	// The same on a JSON reply.
	body := `{"id":"r1","model":"m","status":"completed","output":[{"id":"m1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Alpha beta. ","annotations":[` + url + `]}]}]}`
	evs, err := openairesponses.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, losses := range render(t, roundTripEvents(t, evs)) {
		if strings.HasPrefix(name, "messages") {
			continue
		}
		if hasLossPath(losses, path) {
			t.Errorf("%s (JSON upstream): the delivered citation is reported lost: %v", name, lossPaths(losses))
		}
	}
}
