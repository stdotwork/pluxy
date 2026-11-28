package host

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const CandidatesMinor = 35

func downgradeCandidateCount(minor int, id string, req llm.Request) (llm.Request, []llm.Loss) {
	n := req.Options.CandidateCount
	if minor >= CandidatesMinor || n == 0 {
		return req, nil
	}
	path := llm.CandidateCountPath(req)
	req.Options.CandidateCount = 0
	if origin, ok := originWithCandidateCount(req.Origin, n); ok {
		req.Origin = origin
		return req, nil
	}
	if n <= 1 {
		return req, nil
	}
	detail := fmt.Sprintf("extension %s implements protocol 0.%d, which has no candidate count (0.%d adds it)", id, minor, CandidatesMinor)
	return req, []llm.Loss{{Kind: llm.LossDropped, Path: path, Detail: detail}}
}

func originWithCandidateCount(o *llm.Origin, n int) (*llm.Origin, bool) {
	if o == nil {
		return nil, false
	}
	top := map[string]json.RawMessage{}
	if len(o.Raw) > 0 && json.Unmarshal(o.Raw, &top) != nil {
		return nil, false
	}
	count := json.RawMessage(strconv.Itoa(n))
	switch o.Format {
	case llm.FormatOpenAIChat:
		top["n"] = count
	case llm.FormatGeminiGenerate:
		gen := map[string]json.RawMessage{}
		if raw, ok := top["generationConfig"]; ok && json.Unmarshal(raw, &gen) != nil {
			return nil, false
		}
		gen["candidateCount"] = count
		raw, err := json.Marshal(gen)
		if err != nil {
			return nil, false
		}
		top["generationConfig"] = raw
	default:
		return nil, false
	}
	raw, err := json.Marshal(top)
	if err != nil {
		return nil, false
	}
	out := *o
	out.Raw = raw
	return &out, true
}

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
