package host

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

const webSearchMinor = 29

func downgradeWebSearch(minor int, req llm.Request) llm.Request {
	if minor >= webSearchMinor {
		return req
	}
	hasWebSearch := false
	for _, t := range req.Tools {
		if t.Type == llm.ToolWebSearch {
			hasWebSearch = true
			break
		}
	}
	if !hasWebSearch {
		return req
	}
	tools := make([]llm.Tool, 0, len(req.Tools))
	var chatWebSearch json.RawMessage
	for _, t := range req.Tools {
		if t.Type != llm.ToolWebSearch {
			tools = append(tools, t)
			continue
		}
		if t.Origin != nil && t.Origin.Format == llm.FormatOpenAIChat && t.Origin.Path == "web_search_options" {
			chatWebSearch = t.Origin.Raw
			continue
		}
		tools = append(tools, llm.Tool{Type: llm.ToolNative, Origin: t.Origin})
	}
	if len(tools) == 0 {
		req.Tools = nil
	} else {
		req.Tools = tools
	}
	if len(chatWebSearch) > 0 {
		m := map[string]json.RawMessage{}
		if req.Origin != nil && len(req.Origin.Raw) > 0 {
			_ = json.Unmarshal(req.Origin.Raw, &m)
		}
		m["web_search_options"] = append(json.RawMessage(nil), chatWebSearch...)
		if raw, err := json.Marshal(m); err == nil {
			var o llm.Origin
			if req.Origin != nil {
				o = *req.Origin
			} else {
				o.Format = llm.FormatOpenAIChat
			}
			o.Raw = raw
			req.Origin = &o
		}
	}
	return req
}
