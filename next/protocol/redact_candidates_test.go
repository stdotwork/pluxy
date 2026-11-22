package protocol

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestRedactCandidateEvent(t *testing.T) {
	secret := "sk-secret-cand-token-9876543210"
	r := NewRedactor(&Credential{Kind: CredentialAPIKey, APIKey: secret})

	ev := llm.Event{
		Type: llm.EventCandidate,
		Candidate: &llm.CandidateEvent{
			Index:  1,
			Kind:   llm.CandidateToolCallStart,
			Item:   0,
			CallID: "call_" + secret,
			Name:   "fn_" + secret,
			Delta:  "arg " + secret,
			Media: &llm.OutputMedia{
				Kind:      llm.MediaImage,
				MediaType: "image/png",
				Data:      secret,
			},
			Finish: &llm.Finish{
				Reason:    llm.FinishStop,
				RawReason: "raw_" + secret,
			},
		},
	}
	got := r.Event(ev)
	if got.Candidate == nil {
		t.Fatal("redacted Candidate is nil")
	}
	for name, val := range map[string]string{
		"CallID":    got.Candidate.CallID,
		"Name":      got.Candidate.Name,
		"Delta":     got.Candidate.Delta,
		"MediaData": got.Candidate.Media.Data,
		"RawReason": got.Candidate.Finish.RawReason,
	} {
		if strings.Contains(val, secret) {
			t.Errorf("%s still contains secret: %q", name, val)
		}
	}
	if got.Candidate.Finish.Reason != llm.FinishStop {
		t.Errorf("Finish.Reason = %q, want %q", got.Candidate.Finish.Reason, llm.FinishStop)
	}
	if got.Candidate.Kind != llm.CandidateToolCallStart {
		t.Errorf("Candidate.Kind = %q, want %q", got.Candidate.Kind, llm.CandidateToolCallStart)
	}
}
