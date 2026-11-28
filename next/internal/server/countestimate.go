package server

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/tokenest"
	"github.com/stdotwork/pluxy/next/llm"
)

func estimateInputTokens(req llm.Request) int64 {
	return tokenest.Estimate(estimateText(req))
}

func estimateText(req llm.Request) string {
	var segs []string
	add := func(s ...string) {
		for _, v := range s {
			if v = strings.TrimSpace(v); v != "" {
				segs = append(segs, v)
			}
		}
	}
	var turn, prev string
	for _, it := range req.Items {
		role := ""
		switch it.Type {
		case llm.ItemTypeMessage:
			if it.Role != llm.RoleSystem && it.Role != llm.RoleDeveloper {
				role = it.Role
			}
		case llm.ItemTypeReasoning, llm.ItemTypeToolCall:
			role = llm.RoleAssistant
		case llm.ItemTypeToolResult:
			role = llm.RoleUser
		}
		if role != turn || (it.Type == llm.ItemTypeMessage && prev == llm.ItemTypeMessage) {
			add(role)
		}
		turn, prev = role, it.Type
		switch it.Type {
		case llm.ItemTypeMessage:
			for _, p := range it.Content {
				if p.Type == llm.PartText {
					add(p.Text)
				}
			}
		case llm.ItemTypeReasoning:
			add(it.Text)
		case llm.ItemTypeToolCall:
			add(it.CallID, it.Name, compactJSON(it.Arguments))
		case llm.ItemTypeToolResult:
			add(it.CallID, it.Output)
		}
	}
	for _, t := range req.Tools {
		add(t.Name, t.Description, compactJSON(string(t.Parameters)))
	}
	if c := req.Options.ToolChoice; c != nil {
		add(c.Mode, c.Name)
		add(c.Allowed...)
	}
	return strings.Join(segs, "\n")
}

func compactJSON(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return s
	}
	return b.String()
}
