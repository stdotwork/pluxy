package llm

import (
	"encoding/json"
	"testing"
)

func TestCandidateEventValidationAndSequence(t *testing.T) {
	var seq Sequence
	events := []Event{
		{Type: EventResponseStart, Response: &ResponseInfo{ID: "r1", Model: "m"}},
		{Type: EventItemStart, Index: 0, Item: &ItemStart{Kind: ItemMessage}},
		{Type: EventTextDelta, Index: 0, Delta: "cand0"},
		{Type: EventCandidate, Candidate: &CandidateEvent{Index: 1, Kind: CandidateTextDelta, Delta: "cand1"}},
		{Type: EventCandidate, Candidate: &CandidateEvent{Index: 1, Kind: CandidateReasoningDelta, Delta: "think1"}},
		{Type: EventCandidate, Candidate: &CandidateEvent{Index: 1, Kind: CandidateToolCallStart, Item: 0, CallID: "c1", Name: "get_weather"}},
		{Type: EventCandidate, Candidate: &CandidateEvent{Index: 1, Kind: CandidateToolInputDelta, Item: 0, Delta: `{"city":"NYC"}`}},
		{Type: EventCandidate, Candidate: &CandidateEvent{Index: 1, Kind: CandidateToolCallEnd, Item: 0}},
		{Type: EventCandidate, Candidate: &CandidateEvent{Index: 1, Kind: CandidateFinish, Finish: &Finish{Reason: FinishToolCalls, RawReason: "STOP"}}},
		{Type: EventItemEnd, Index: 0},
		{Type: EventFinish, Finish: &Finish{Reason: FinishStop}},
	}
	for i, ev := range events {
		if err := seq.Check(ev); err != nil {
			t.Fatalf("event[%d] (%s): unexpected sequence error: %v", i, ev.Type, err)
		}
		if ev.Type == EventCandidate {
			if err := ValidCandidateEvent(ev.Candidate); err != nil {
				t.Fatalf("event[%d]: unexpected ValidCandidateEvent error: %v", i, err)
			}
		}
	}

	// JSON round-trip preserves CandidateCount and CandidateEvent; empty lists parse to nil.
	req := Request{
		Model:   "m",
		Options: Options{CandidateCount: 3},
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var gotReq Request
	if err := json.Unmarshal(b, &gotReq); err != nil {
		t.Fatal(err)
	}
	if gotReq.Options.CandidateCount != 3 {
		t.Errorf("CandidateCount = %d, want 3", gotReq.Options.CandidateCount)
	}
	if gotReq.Options.Stop != nil || gotReq.Tools != nil {
		t.Errorf("empty lists did not parse to nil: stop=%v tools=%v", gotReq.Options.Stop, gotReq.Tools)
	}

	// Invalid candidate events must be rejected.
	invalid := []struct {
		name string
		c    *CandidateEvent
	}{
		{"nil", nil},
		{"index 0", &CandidateEvent{Index: 0, Kind: CandidateTextDelta, Delta: "x"}},
		{"negative index", &CandidateEvent{Index: -1, Kind: CandidateTextDelta, Delta: "x"}},
		{"unknown kind", &CandidateEvent{Index: 1, Kind: "bogus"}},
		{"tool start without name", &CandidateEvent{Index: 1, Kind: CandidateToolCallStart, CallID: "c1"}},
		{"tool start without call_id", &CandidateEvent{Index: 1, Kind: CandidateToolCallStart, Name: "fn"}},
		{"finish without finish", &CandidateEvent{Index: 1, Kind: CandidateFinish}},
		{"media without media", &CandidateEvent{Index: 1, Kind: CandidateMedia}},
	}
	for _, tc := range invalid {
		if err := ValidCandidateEvent(tc.c); err == nil {
			t.Errorf("%s: ValidCandidateEvent returned nil, want error", tc.name)
		}
		var s Sequence
		_ = s.Check(Event{Type: EventResponseStart, Response: &ResponseInfo{ID: "r", Model: "m"}})
		if err := s.Check(Event{Type: EventCandidate, Candidate: tc.c}); err == nil {
			t.Errorf("%s: Sequence.Check returned nil, want error", tc.name)
		}
	}
}

func TestCandidateCountPath(t *testing.T) {
	if got := CandidateCountPath(Request{Native: &Native{Format: FormatOpenAIChat}}); got != "n" {
		t.Errorf("chat path = %q, want n", got)
	}
	if got := CandidateCountPath(Request{Native: &Native{Format: FormatGeminiGenerate}}); got != "generationConfig.candidateCount" {
		t.Errorf("gemini path = %q, want generationConfig.candidateCount", got)
	}
	if got := CandidateCountPath(Request{}); got != "candidate_count" {
		t.Errorf("default path = %q, want candidate_count", got)
	}
}
