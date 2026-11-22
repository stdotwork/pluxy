package conformance

import (
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformanceCitations(t *testing.T) {
	if protocol.Minor < host.CitationsMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.CitationsMinor)
	}
	s, e := 0, 5
	valid := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "Café!"},
		{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://example.com/a", Title: "A", StartIndex: &s, EndIndex: &e}}},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Sources: llm.Sources{{URL: "https://example.com/a", Title: "A"}}},
	}
	if err := checkReplyCitations(valid); err != nil {
		t.Fatalf("checkReplyCitations(valid) = %v, want nil", err)
	}

	neg, over := -1, 6
	invalidCases := []struct {
		name string
		evs  []llm.Event
	}{
		{
			name: "non-http citation url",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
				{Type: llm.EventTextDelta, Index: 0, Delta: "Hi"},
				{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "javascript:alert(1)"}}},
			},
		},
		{
			name: "citation on reasoning item",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemReasoning}},
				{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://example.com"}}},
			},
		},
		{
			name: "half-set range",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
				{Type: llm.EventTextDelta, Index: 0, Delta: "Café!"},
				{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://example.com", StartIndex: &s}}},
			},
		},
		{
			name: "range past rune length",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
				{Type: llm.EventTextDelta, Index: 0, Delta: "Café!"},
				{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://example.com", StartIndex: &s, EndIndex: &over}}},
			},
		},
		{
			name: "negative start index",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
				{Type: llm.EventTextDelta, Index: 0, Delta: "Café!"},
				{Type: llm.EventItemEnd, Index: 0, Citations: llm.Citations{{URL: "https://example.com", StartIndex: &neg, EndIndex: &e}}},
			},
		},
		{
			name: "sources on non-finish",
			evs: []llm.Event{
				{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}, Sources: llm.Sources{{URL: "https://example.com"}}},
			},
		},
		{
			name: "non-http source url",
			evs: []llm.Event{
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Sources: llm.Sources{{URL: "data:text/plain,x"}}},
			},
		},
	}
	for _, tc := range invalidCases {
		if err := checkReplyCitations(tc.evs); err == nil {
			t.Errorf("%s: checkReplyCitations returned nil, want error", tc.name)
		}
	}
}
