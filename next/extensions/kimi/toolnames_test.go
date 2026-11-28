package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestReplyNamesRestoresClientToolNames(t *testing.T) {
	req := llm.Request{Tools: []llm.Tool{{Name: "mcp:server.tool"}, {Name: "get_weather"}}}
	tr := &llm.Translation{To: llm.FormatOpenAIChat, From: llm.FormatAnthropicMessages}
	names := replyNames(req, tr)
	if len(names) != 1 || names["mcp_server_tool"] != "mcp:server.tool" {
		t.Errorf("names = %v, want mcp_server_tool back to mcp:server.tool", names)
	}
	ev := llm.Event{Type: llm.EventItemStart, Item: &llm.ItemStart{
		Kind: llm.ItemToolCall, CallID: "c1", Name: "mcp_server_tool"}}
	(&reporting{names: names}).restore(&ev)
	if got := ev.Item.Name; got != "mcp:server.tool" {
		t.Errorf("sent name = %q, want the client's", got)
	}
	ev = llm.Event{Type: llm.EventItemStart, Item: &llm.ItemStart{
		Kind: llm.ItemToolCall, CallID: "c1", Name: "get_weather"}}
	(&reporting{names: names}).restore(&ev)
	if got := ev.Item.Name; got != "get_weather" {
		t.Errorf("unchanged name became %q", got)
	}
	if names := replyNames(req, &llm.Translation{To: llm.FormatOpenAIChat, Passthrough: true}); names != nil {
		t.Errorf("passthrough names = %v, want none", names)
	}
}
