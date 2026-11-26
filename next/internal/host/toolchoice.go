package host

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// allowedToolsMinor is the protocol minor version that adds a tool choice's allowed tool list
// (llm.ToolChoice.Allowed).
const allowedToolsMinor = 26

// downgradeAllowedTools returns req as an extension that implements protocol minor version minor
// can take it. Before allowedToolsMinor a tool choice keeps its mode and name but loses the tools
// the model may choose from, so core narrows the request the way a format without an allowed list
// does: the request keeps only the function tools the list names, and every other tool goes,
// hosted and native ones included. The model can then only call the allowed ones, which is what the
// list asks for, so nothing is reported.
//
// A list naming no function tool of the request leaves nothing to narrow to, and sending the
// request whole would let the model call any tool: it is an invalid request, refused before it is
// sent. req's own choice and tools are left as they are; a narrowed request gets its own copies of
// them.
func downgradeAllowedTools(minor int, req llm.Request) (llm.Request, *llm.Error) {
	tc := req.Options.ToolChoice
	if minor >= allowedToolsMinor || tc == nil || len(tc.Allowed) == 0 {
		return req, nil
	}
	allowed := make(map[string]bool, len(tc.Allowed))
	for _, name := range tc.Allowed {
		allowed[name] = true
	}
	tools := make([]llm.Tool, 0, len(req.Tools))
	for _, t := range req.Tools {
		if !t.IsFunction() || !allowed[t.Name] {
			continue
		}
		tools = append(tools, t)
	}
	if len(tools) == 0 {
		// No function tool the list names is defined, so no narrowing carries it: every allowed
		// name matches no tool of the request.
		return req, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
			Message: fmt.Sprintf("tool_choice allows no function tool this request defines: %s", strings.Join(tc.Allowed, ", "))}
	}
	choice := *tc
	choice.Allowed = nil
	req.Tools = tools
	req.Options.ToolChoice = &choice
	return req, nil
}
