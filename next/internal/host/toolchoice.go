package host

import (
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// allowedToolsMinor is the protocol minor version that adds a tool choice's allowed tool list
// (llm.ToolChoice.Allowed).
const allowedToolsMinor = 26

// allowedToolsPath is the request field an allowed tool list comes from, for a loss.
const allowedToolsPath = "options.tool_choice.allowed"

// downgradeAllowedTools returns req as an extension that implements protocol minor version minor
// can take it. Before allowedToolsMinor a tool choice keeps its mode and name and loses the tools
// the model may choose from, returned as a dropped loss: an extension that cannot read the list
// would let the model call any tool it has without saying so. req's own choice is left as it is;
// a changed request gets its own copy of it.
func downgradeAllowedTools(minor int, id string, req llm.Request) (llm.Request, []llm.Loss) {
	tc := req.Options.ToolChoice
	if minor >= allowedToolsMinor || tc == nil || len(tc.Allowed) == 0 {
		return req, nil
	}
	detail := fmt.Sprintf("extension %s implements protocol 0.%d, which has no tool choice allowed list (0.%d adds it)", id, minor, allowedToolsMinor)
	choice := *tc
	choice.Allowed = nil
	req.Options.ToolChoice = &choice
	return req, []llm.Loss{{Kind: llm.LossDropped, Path: allowedToolsPath, Detail: detail}}
}

// refuseAllowedTools returns the error for a request whose tool choice lost its allowed list on
// the way to an extension before allowedToolsMinor, when its drop policy is reject; otherwise nil.
func refuseAllowedTools(id string, minor int, req llm.Request, lost []llm.Loss) *llm.Error {
	if req.DropPolicy != llm.DropReject || len(lost) == 0 {
		return nil
	}
	paths := make([]string, 0, len(lost))
	for _, l := range lost {
		paths = append(paths, l.Path)
	}
	return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
		Message: fmt.Sprintf("extension %s implements protocol 0.%d, which cannot carry the tools a tool choice allows (0.%d adds them): %s",
			id, minor, allowedToolsMinor, strings.Join(paths, ", "))}
}
