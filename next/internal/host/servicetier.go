package host

import (
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const serviceTierMinor = 28

func downgradeServiceTier(minor int, id string, req llm.Request) (llm.Request, []llm.Loss) {
	if minor >= serviceTierMinor || (req.Options.ServiceTier == "" && req.Options.Verbosity == "") {
		return req, nil
	}
	detail := fmt.Sprintf("extension %s implements protocol 0.%d, which has no service tier or verbosity (0.%d adds them)", id, minor, serviceTierMinor)
	var losses []llm.Loss
	if req.Options.ServiceTier != "" {
		losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: llm.ServiceTierPath(req), Detail: detail})
		req.Options.ServiceTier = ""
	}
	if req.Options.Verbosity != "" {
		losses = append(losses, llm.Loss{Kind: llm.LossDropped, Path: llm.VerbosityPath(req), Detail: detail})
		req.Options.Verbosity = ""
	}
	return req, losses
}

func refuseServiceTier(id string, minor int, req llm.Request, lost []llm.Loss) *llm.Error {
	if req.DropPolicy != llm.DropReject || len(lost) == 0 {
		return nil
	}
	paths := make([]string, 0, len(lost))
	for _, l := range lost {
		paths = append(paths, l.Path)
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
		Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot carry a service tier or verbosity (0.%d adds them): %s",
			id, minor, serviceTierMinor, strings.Join(paths, ", "))}
}
