package host

import "github.com/stdotwork/pluxy/next/llm"

// webSearchMinor is the protocol minor version that adds the canonical web search tool
// (llm.ToolWebSearch and llm.Tool.WebSearch).
const webSearchMinor = 29

// downgradeWebSearch returns req as an extension that implements protocol minor version minor
// can take it. Before webSearchMinor, every ToolWebSearch becomes a ToolNative with its Origin
// unchanged, matching what extensions below 0.29 received before canonical web search tools
// existed. req's own tool slice is not mutated.
func downgradeWebSearch(minor int, req llm.Request) llm.Request {
	if minor >= webSearchMinor {
		return req
	}
	var tools []llm.Tool
	for i, t := range req.Tools {
		if t.Type != llm.ToolWebSearch {
			continue
		}
		if tools == nil {
			tools = make([]llm.Tool, len(req.Tools))
			copy(tools, req.Tools)
		}
		tools[i] = llm.Tool{Type: llm.ToolNative, Origin: t.Origin}
	}
	if tools != nil {
		req.Tools = tools
	}
	return req
}
