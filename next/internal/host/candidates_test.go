package host

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestCandidatesMinorGating(t *testing.T) {
	if protocol.Minor < CandidatesMinor {
		t.Fatalf("protocol.Minor = %d, want >= %d", protocol.Minor, CandidatesMinor)
	}

	req := llm.Request{
		Model:      "m",
		DropPolicy: llm.DropReport,
		Native:     &llm.Native{Format: llm.FormatOpenAIChat},
		Options:    llm.Options{CandidateCount: 3},
	}
	gotOld, lost := downgradeCandidateCount(CandidatesMinor-1, "ext-old", req)
	if gotOld.Options.CandidateCount != 0 {
		t.Errorf("downgraded CandidateCount = %d, want 0", gotOld.Options.CandidateCount)
	}
	if len(lost) != 1 || lost[0].Kind != llm.LossDropped || lost[0].Path != "n" {
		t.Errorf("lost = %+v, want dropped loss at n", lost)
	}
	if req.Options.CandidateCount != 3 {
		t.Errorf("caller request mutated: CandidateCount = %d", req.Options.CandidateCount)
	}

	reqReject := req
	reqReject.DropPolicy = llm.DropReject
	if err := refuseCandidateCount("ext-old", CandidatesMinor-1, reqReject, lost); err == nil || err.Code != llm.CodeUnsupported || !reflect.DeepEqual(err.Dropped, []string{"n"}) {
		t.Errorf("refuseCandidateCount = %+v, want unsupported naming [n]", err)
	}

	gotNew, lostNew := downgradeCandidateCount(CandidatesMinor, "ext-new", req)
	if gotNew.Options.CandidateCount != 3 || len(lostNew) != 0 {
		t.Errorf("at minor %d: CandidateCount=%d lost=%+v", CandidatesMinor, gotNew.Options.CandidateCount, lostNew)
	}

	reqOne := req
	reqOne.Options.CandidateCount = 1
	if _, l := downgradeCandidateCount(CandidatesMinor-1, "ext-old", reqOne); len(l) != 0 {
		t.Errorf("count 1 produced losses: %+v", l)
	}

	ev := llm.Event{
		Type:      llm.EventCandidate,
		Candidate: &llm.CandidateEvent{Index: 1, Kind: llm.CandidateTextDelta, Delta: "hi"},
		Raw:       json.RawMessage(`{"candidates":[]}`),
	}
	oldExt := &Extension{info: protocol.Info{ProtocolMinor: CandidatesMinor - 1}}
	normOld := oldExt.normalizeEvent(ev)
	if normOld.Candidate != nil {
		t.Errorf("minor %d kept Candidate: %+v, want nil", CandidatesMinor-1, normOld.Candidate)
	}
	if normOld.Type != llm.EventNative {
		t.Errorf("minor %d with Raw: type = %q, want %q", CandidatesMinor-1, normOld.Type, llm.EventNative)
	}

	newExt := &Extension{info: protocol.Info{ProtocolMinor: CandidatesMinor}}
	normNew := newExt.normalizeEvent(ev)
	if normNew.Type != llm.EventCandidate || normNew.Candidate == nil || normNew.Candidate.Delta != "hi" {
		t.Errorf("minor %d Candidate = %+v, want preserved", CandidatesMinor, normNew.Candidate)
	}
}
