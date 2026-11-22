package conformance

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestConformanceCandidates(t *testing.T) {
	if protocol.Minor < host.CandidatesMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, host.CandidatesMinor)
	}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))

	valid := []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "a"},
		{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateTextDelta, Delta: "b"}},
		{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: png}}},
		{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateFinish, Finish: &llm.Finish{Reason: llm.FinishStop}}},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
	if err := checkReplyCandidates(valid); err != nil {
		t.Fatalf("checkReplyCandidates(valid) = %v, want nil", err)
	}

	invalidCases := []struct {
		name string
		evs  []llm.Event
	}{
		{
			name: "candidate on finish",
			evs: []llm.Event{
				{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateTextDelta, Delta: "x"}},
			},
		},
		{
			name: "candidate event with nil Candidate",
			evs:  []llm.Event{{Type: llm.EventCandidate}},
		},
		{
			name: "candidate index 0",
			evs:  []llm.Event{{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 0, Kind: llm.CandidateTextDelta, Delta: "x"}}},
		},
		{
			name: "candidate media exceeds reply limit",
			evs: []llm.Event{
				{Type: llm.EventMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: strings.Repeat("A", llm.MaxOutputMediaPartBytes)}},
				{Type: llm.EventCandidate, Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateMedia, Media: &llm.OutputMedia{Kind: llm.MediaImage, MediaType: "image/png", Data: strings.Repeat("A", llm.MaxOutputMediaReplyBytes)}}},
			},
		},
	}
	for _, tc := range invalidCases {
		if err := checkReplyCandidates(tc.evs); err == nil {
			t.Errorf("%s: checkReplyCandidates returned nil, want error", tc.name)
		}
	}
}
