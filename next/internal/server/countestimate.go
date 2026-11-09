package server

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/tokenest"
	"github.com/stdotwork/pluxy/next/llm"
)

// estimateInputTokens estimates the input tokens of req, for a token count no account can give.
// It counts the same text today's core's local counter counts: the system text; for each message
// its role and text; reasoning text; each tool call's ID, name and arguments; each tool result's
// call ID and output; each tool's name, description and parameters; and the tool choice. Images
// and files add nothing. Every piece is trimmed, empty ones are dropped and the rest are joined
// with line breaks, then estimated as one text. One estimate serves every provider: it follows
// o200k_base, which today's core uses for Claude, xAI and Meta, and not the other tokenizers its
// OpenAI-compatible and Codex counts use.
func estimateInputTokens(req llm.Request) int64 {
	return tokenest.Estimate(estimateText(req))
}

// estimateText returns the text estimateInputTokens estimates.
func estimateText(req llm.Request) string {
	var segs []string
	add := func(s ...string) {
		for _, v := range s {
			if v = strings.TrimSpace(v); v != "" {
				segs = append(segs, v)
			}
		}
	}
	// A message's role is counted once, also when the message was split into several items:
	// an assistant message's reasoning, text and calls, or the results in a user message.
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
	}
	return strings.Join(segs, "\n")
}

// compactJSON returns s without insignificant whitespace when it is JSON, otherwise s.
func compactJSON(s string) string {
	var b bytes.Buffer
	if json.Compact(&b, []byte(s)) != nil {
		return s
	}
	return b.String()
}
