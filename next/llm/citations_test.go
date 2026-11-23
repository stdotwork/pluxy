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
	if b.AddSource(llm.Source{URL: "https://s.example/1"}) {
		t.Error("a source fit after a citation did not")
	}
	if got := b.WithLoss(nil); len(got) != 1 || got[0].Path != "citations" || got[0].Kind != llm.LossDropped {
		t.Errorf("losses = %+v, want one dropped loss at citations", got)
	}
}
