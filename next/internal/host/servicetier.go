package host

import (
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// serviceTierMinor is the protocol minor version that adds a request's service tier and
// verbosity (llm.Options.ServiceTier and llm.Options.Verbosity).
const serviceTierMinor = 28

// downgradeServiceTier returns req as an extension that implements protocol minor version minor
// can take it. Before serviceTierMinor the request's service tier and verbosity are not sent:
// an extension that cannot read them would leave them out without saying so (they are no longer
// in the request's kept fields, where an extension before 0.28 would have found and reported
// them). Each is returned as a dropped loss, at the field the client wrote it in. req's own
// options are left as they are; a changed request has its own.
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

// refuseServiceTier returns the error for a request whose service tier or verbosity was left out
// on the way to an extension before serviceTierMinor, when its drop policy is reject; otherwise
// nil.
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
