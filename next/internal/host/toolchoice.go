package host

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const allowedToolsMinor = 26

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
		return req, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
			Message: fmt.Sprintf("tool_choice allows no function tool this request defines: %s", strings.Join(tc.Allowed, ", "))}
	}
	choice := *tc
	choice.Allowed = nil
	req.Tools = tools
	req.Options.ToolChoice = &choice
	return req, nil
}
