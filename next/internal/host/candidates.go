package host

import (
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// CandidatesMinor is the protocol minor version that adds multi-candidate requests
// (llm.Options.CandidateCount) and candidate reply events (llm.EventCandidate and
// llm.Event.Candidate).
const CandidatesMinor = 34

// downgradeCandidateCount returns req as an extension that implements protocol minor version
// minor can take it. Before CandidatesMinor a candidate count > 1 is not sent: an extension that
// cannot read it would leave it out without saying so. It is returned as a dropped loss at the
// field the client wrote it in.
func downgradeCandidateCount(minor int, id string, req llm.Request) (llm.Request, []llm.Loss) {
	if minor >= CandidatesMinor || req.Options.CandidateCount <= 1 {
		return req, nil
	}
	detail := fmt.Sprintf("extension %s implements protocol 0.%d, which has no candidate count (0.%d adds it)", id, minor, CandidatesMinor)
	loss := llm.Loss{Kind: llm.LossDropped, Path: llm.CandidateCountPath(req), Detail: detail}
	req.Options.CandidateCount = 0
	return req, []llm.Loss{loss}
}

// refuseCandidateCount returns the error for a request whose candidate count was left out on the
// way to an extension before CandidatesMinor, when its drop policy is reject; otherwise nil.
func refuseCandidateCount(id string, minor int, req llm.Request, lost []llm.Loss) *llm.Error {
	if req.DropPolicy != llm.DropReject || len(lost) == 0 {
		return nil
	}
	paths := make([]string, 0, len(lost))
	for _, l := range lost {
		paths = append(paths, l.Path)
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
		Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot carry a candidate count (0.%d adds it): %s",
			id, minor, CandidatesMinor, strings.Join(paths, ", "))}
}
