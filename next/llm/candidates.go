package llm

import "fmt"

// Candidate event kinds (CandidateEvent.Kind, protocol minor version 35).
const (
	CandidateTextDelta      = "text.delta"
	CandidateReasoningDelta = "reasoning.delta"
	CandidateToolCallStart  = "tool_call.start"
	CandidateToolInputDelta = "tool_input.delta"
	CandidateToolCallEnd    = "tool_call.end"
	CandidateMedia          = "media"
	CandidateFinish         = "finish"
)

// CandidateEvent is one step of a candidate reply after candidate 0 (Index >= 1), added in
// protocol minor version 35. Candidate 0 continues to use the top-level item, delta, media and
// finish events unchanged so consumers that show only one candidate never see or mix in later
// candidates.
type CandidateEvent struct {
	// Index is the 1-based candidate index (must be >= 1).
	Index int `json:"index"`
	// Kind selects which fields apply (the Candidate* constants).
	Kind string `json:"kind"`
	// Item identifies a tool call within this candidate for tool_call.start, tool_input.delta
	// and tool_call.end.
	Item int `json:"item,omitempty"`
	// Delta carries text, reasoning or tool argument fragments.
	Delta string `json:"delta,omitempty"`
	// CallID and Name identify a tool call on tool_call.start.
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
	// Media carries one generated image or audio part on media.
	Media *OutputMedia `json:"media,omitempty"`
	// Finish carries this candidate's own finish reason on finish.
	Finish *Finish `json:"finish,omitempty"`
}

// ValidCandidateEvent checks that c satisfies the protocol 0.35 invariants for a candidate event.
func ValidCandidateEvent(c *CandidateEvent) error {
	if c == nil {
		return fmt.Errorf("candidate event without candidate payload")
	}
	if c.Index < 1 {
		return fmt.Errorf("candidate index %d must be >= 1", c.Index)
	}
	switch c.Kind {
	case CandidateTextDelta, CandidateReasoningDelta:
		return nil
	case CandidateToolCallStart:
		if c.CallID == "" || c.Name == "" {
			return fmt.Errorf("candidate %d tool_call.start without call_id or name", c.Index)
		}
		return nil
	case CandidateToolInputDelta, CandidateToolCallEnd:
		return nil
	case CandidateMedia:
		return ValidOutputMedia(c.Media)
	case CandidateFinish:
		if c.Finish == nil {
			return fmt.Errorf("candidate %d finish without finish reason", c.Index)
		}
		return nil
	default:
		return fmt.Errorf("candidate %d has unknown kind %q", c.Index, c.Kind)
	}
}

// CandidateCountPath returns the field of r's source body that Options.CandidateCount came from,
// for a loss: n in a Chat Completions request, generationConfig.candidateCount in a Gemini
// GenerateContent request, and candidate_count otherwise.
func CandidateCountPath(r Request) string {
	switch r.SourceFormat() {
	case FormatOpenAIChat:
		return "n"
	case FormatGeminiGenerate:
		return "generationConfig.candidateCount"
	}
	return "candidate_count"
}

// CandidateCountUnsent is the detail of the loss for a candidate count an encoder leaves out
// because nothing says its upstream takes the field.
const CandidateCountUnsent = "nothing says this upstream takes a candidate count, so it is not sent"
