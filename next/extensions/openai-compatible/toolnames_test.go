package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestReplyNamesRestoresClientToolNames(t *testing.T) {
	req := llm.Request{
		Tools: []llm.Tool{{Name: "mcp/server.tool"}, {Name: "get_weather"}},
	}
	for _, c := range []struct {
		to, up, reply string
	}{
		{llm.FormatOpenAIChat, "mcp_server_tool", "mcp/server.tool"},
		{llm.FormatGeminiGenerate, "mcp_server.tool", "mcp/server.tool"},
		{llm.FormatAnthropicMessages, "mcp_server_tool", "mcp/server.tool"},
	} {
		tr := &llm.Translation{To: c.to, From: llm.FormatAnthropicMessages}
		names := replyNames(req, tr)
		if len(names) != 1 || names[c.up] != c.reply {
			t.Errorf("%s: names = %v, want %s back to %q", c.to, names, c.up, c.reply)
		}
		ev := llm.Event{Type: llm.EventItemStart, Item: &llm.ItemStart{
			Kind: llm.ItemToolCall, CallID: "c1", Name: c.up}}
		(&reporting{names: names}).restore(&ev)
		if got := ev.Item.Name; got != c.reply {
			t.Errorf("%s: sent name = %q, want %q", c.to, got, c.reply)
		}
		ev = llm.Event{Type: llm.EventItemStart, Item: &llm.ItemStart{
			Kind: llm.ItemToolCall, CallID: "c1", Name: "get_weather"}}
		(&reporting{names: names}).restore(&ev)
		if got := ev.Item.Name; got != "get_weather" {
			t.Errorf("%s: unchanged name became %q", c.to, got)
		}
	}
	if names := replyNames(req, &llm.Translation{To: llm.FormatOpenAIChat, Passthrough: true}); names != nil {
		t.Errorf("passthrough names = %v, want none", names)
	}
	if names := replyNames(req, &llm.Translation{To: llm.FormatOpenAIResponses}); names != nil {
		t.Errorf("Responses target names = %v, want none", names)
	}
}
