package llm_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestEventCitationsAndSourcesJSONRoundTrip(t *testing.T) {
	start, end := 0, 5
	endEv := llm.Event{
		Type:  llm.EventItemEnd,
		Index: 0,
		Citations: []llm.Citation{{
			URL:        "https://example.com/a",
			Title:      "Example A",
			StartIndex: &start,
			EndIndex:   &end,
		}},
	}
	b, err := json.Marshal(endEv)
	if err != nil {
		t.Fatalf("marshal item.end: %v", err)
	}
	var gotEnd llm.Event
	if err := json.Unmarshal(b, &gotEnd); err != nil {
		t.Fatalf("unmarshal item.end: %v", err)
	}
	if len(gotEnd.Citations) != 1 || gotEnd.Citations[0].URL != "https://example.com/a" ||
		gotEnd.Citations[0].Title != "Example A" ||
		gotEnd.Citations[0].StartIndex == nil || *gotEnd.Citations[0].StartIndex != 0 ||
		gotEnd.Citations[0].EndIndex == nil || *gotEnd.Citations[0].EndIndex != 5 {
		t.Fatalf("round-trip citations = %+v", gotEnd.Citations)
	}

	finEv := llm.Event{
		Type:   llm.EventFinish,
		Finish: &llm.Finish{Reason: llm.FinishStop},
		Sources: []llm.Source{{
			URL:   "https://example.com/a",
			Title: "Example A",
		}},
	}
	b, err = json.Marshal(finEv)
	if err != nil {
		t.Fatalf("marshal finish: %v", err)
	}
	var gotFin llm.Event
	if err := json.Unmarshal(b, &gotFin); err != nil {
		t.Fatalf("unmarshal finish: %v", err)
	}
	if len(gotFin.Sources) != 1 || gotFin.Sources[0].URL != "https://example.com/a" || gotFin.Sources[0].Title != "Example A" {
		t.Fatalf("round-trip sources = %+v", gotFin.Sources)
	}
}

func TestEventEmptyCitationListsParseToNil(t *testing.T) {
	var ev llm.Event
	if err := json.Unmarshal([]byte(`{"type":"item.end","index":0,"citations":[],"sources":[]}`), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Citations != nil {
		t.Errorf("empty citations = %#v, want nil", ev.Citations)
	}
	if ev.Sources != nil {
		t.Errorf("empty sources = %#v, want nil", ev.Sources)
	}
}

func TestValidCitationURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://example.com/path?q=1#frag", true},
		{"http://example.com", true},
		{"HTTPS://Example.COM/page", true},
		{"https://vertexaisearch.cloud.google.com/grounding-api-redirect/abc123", true},
		{"javascript:alert(1)", false},
		{"data:text/html,hi", false},
		{"file:///etc/passwd", false},
		{"ftp://example.com/file", false},
		{"/relative/path", false},
		{"https://", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := llm.ValidCitationURL(tc.url); got != tc.want {
			t.Errorf("ValidCitationURL(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestUTF8ByteAndRuneOffsets(t *testing.T) {
	// "café 東京 🚀!"
	// c(1) a(1) f(1) é(2) ' '(1) 東(3) 京(3) ' '(1) 🚀(4) !(1) = 18 bytes, 10 runes
	s := "café 東京 🚀!"
	pairs := []struct {
		byteOff int
		runeOff int
	}{
		{0, 0},
		{3, 3},   // start of 'é'
		{5, 4},   // space after 'é'
		{6, 5},   // start of '東'
		{9, 6},   // start of '京'
		{12, 7},  // space after '京'
		{13, 8},  // start of '🚀'
		{17, 9},  // '!'
		{18, 10}, // end
	}
	for _, p := range pairs {
		gotR, ok := llm.UTF8ByteToRuneOffset(s, p.byteOff)
		if !ok || gotR != p.runeOff {
			t.Errorf("UTF8ByteToRuneOffset(%d) = (%d, %v), want (%d, true)", p.byteOff, gotR, ok, p.runeOff)
		}
		gotB, ok := llm.RuneToUTF8ByteOffset(s, p.runeOff)
		if !ok || gotB != p.byteOff {
			t.Errorf("RuneToUTF8ByteOffset(%d) = (%d, %v), want (%d, true)", p.runeOff, gotB, ok, p.byteOff)
		}
	}
	// Mid-rune byte offsets and out-of-range offsets must fail.
	for _, bad := range []int{-1, 4, 7, 8, 14, 15, 16, 19} {
		if _, ok := llm.UTF8ByteToRuneOffset(s, bad); ok {
			t.Errorf("UTF8ByteToRuneOffset(%d) succeeded, want false", bad)
		}
	}
	for _, bad := range []int{-1, 11} {
		if _, ok := llm.RuneToUTF8ByteOffset(s, bad); ok {
			t.Errorf("RuneToUTF8ByteOffset(%d) succeeded, want false", bad)
		}
	}
}

func TestCiteBudgetKeepsWhatFitsAndReportsTheRest(t *testing.T) {
	var b llm.CiteBudget
	if got := b.WithLoss(nil); len(got) != 0 {
		t.Fatalf("losses with nothing left out = %+v", got)
	}
	big := strings.Repeat("t", llm.MaxCitationBytes/4)
	cs := []llm.Citation{{URL: "https://a.example/1", Title: big}, {URL: "https://a.example/2", Title: big},
		{URL: "https://a.example/3", Title: big}, {URL: "https://a.example/4", Title: big}}
	kept := b.KeepCitations(cs)
	if len(kept) != 3 {
		t.Errorf("kept %d of 4 citations of a quarter of the limit each, want 3 (their URLs and overhead take the fourth's room)", len(kept))
	}
	if !b.AddSource(llm.Source{URL: "https://s.example/1"}) {
		t.Error("a source did not fit after citations filled their own room: each has a limit of its own")
	}
	if got := b.WithLoss(nil); len(got) != 1 || got[0].Path != "citations" || got[0].Kind != llm.LossDropped {
		t.Errorf("losses = %+v, want one dropped loss at citations", got)
	}
}

func TestReplyLossesSparesAClientOfTheUpstreamsFormat(t *testing.T) {
	losses := []llm.Loss{
		{Kind: llm.LossDropped, Path: llm.LossPathCitations, Detail: "x"},
		{Kind: llm.LossDropped, Path: llm.LossPathFinish, Detail: "x"},
		{Kind: llm.LossDropped, Path: "output[0]", Detail: "x"},
	}
	if got := llm.ReplyLosses(losses, false); len(got) != 3 {
		t.Errorf("a client of another format loses %+v, want all three", got)
	}
	if got := llm.ReplyLosses(losses, true); len(got) != 1 || got[0].Path != "output[0]" {
		t.Errorf("a client of the upstream's format loses %+v, want only the media", got)
	}
	if len(losses) != 3 {
		t.Error("ReplyLosses changed its argument")
	}
}

// A client the upstream's body reaches has every candidate after the first whole, so it loses
// nothing at one; candidate 0 and what is not a candidate stay reported.
func TestReplyLossesSparesLaterCandidatesOfAClientTheBodyReaches(t *testing.T) {
	for path, spared := range map[string]bool{
		"candidates[1].groundingMetadata":          true,
		"candidates[12].content.parts[0].fileData": true,
		"choices[2].message.annotations":           true,
		"candidates[0].content.parts[0].fileData":  false,
		"choices[0].message.annotations":           false,
		"candidates":                               false,
		"candidates[x]":                            false,
		"candidates[]":                             false,
		"candidates[01":                            false,
		"output[1]":                                false,
	} {
		got := llm.ReplyLosses([]llm.Loss{{Kind: llm.LossDropped, Path: path}}, true)
		if (len(got) == 0) != spared {
			t.Errorf("%s: spared = %v, want %v", path, len(got) == 0, spared)
		}
		if got := llm.ReplyLosses([]llm.Loss{{Kind: llm.LossDropped, Path: path}}, false); len(got) != 1 {
			t.Errorf("%s: a client of another format must hear of it", path)
		}
	}
}

func TestBodyReachesClient(t *testing.T) {
	req := func(format string, stream bool) *llm.Request {
		return &llm.Request{Stream: stream, Native: &llm.Native{Format: format}}
	}
	for _, c := range []struct {
		format                   string
		clientStreams, upStreams bool
		want                     bool
	}{
		{llm.FormatAnthropicMessages, false, true, true},
		{llm.FormatAnthropicMessages, true, false, true},
		{llm.FormatOpenAIResponses, true, true, true},
		{llm.FormatOpenAIResponses, false, true, true},
		{llm.FormatOpenAIResponses, false, false, true},
		{llm.FormatOpenAIResponses, true, false, false}, // a stream built from a whole reply
		{llm.FormatGeminiGenerate, true, true, true},
		{llm.FormatGeminiGenerate, true, false, true},
		{llm.FormatGeminiGenerate, false, false, true},
		{llm.FormatGeminiGenerate, false, true, false}, // a whole reply built from a stream
	} {
		if got := req(c.format, c.clientStreams).BodyReachesClient(c.format, c.upStreams); got != c.want {
			t.Errorf("%s, client streams %v, upstream streams %v: %v, want %v", c.format, c.clientStreams, c.upStreams, got, c.want)
		}
	}
	if req(llm.FormatOpenAIResponses, false).BodyReachesClient(llm.FormatGeminiGenerate, false) {
		t.Error("a request of another format gets the body")
	}
	if (&llm.Request{}).BodyReachesClient(llm.FormatOpenAIResponses, false) || (*llm.Request)(nil).BodyReachesClient("x", false) {
		t.Error("a request with no native body gets the body")
	}
}
