package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestReplyNamesRestoresClientToolNames(t *testing.T) {
	req := llm.Request{
		Tools: []llm.Tool{{Name: "mcp/server.tool"}, {Name: "get_weather"}},
		Items: []llm.Item{{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "called/only"}},
	}
	tr := &llm.Translation{To: llm.FormatGeminiGenerate, From: llm.FormatAnthropicMessages}
	names := replyNames(req, tr)
	if names["mcp_server.tool"] != "mcp/server.tool" {
		t.Errorf("names = %v, want mcp_server.tool back to mcp/server.tool", names)
	}
	if names["called_only"] != "called/only" {
		t.Errorf("names = %v, want called_only back to called/only", names)
	}
	if len(names) != 2 {
		t.Errorf("names = %v, want only the changed names", names)
	}

	rec := &recorder{}
	r := &reporting{out: rec, tr: tr, names: names}
	if err := r.Send(llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{
		Kind: llm.ItemToolCall, CallID: "c1", Name: "mcp_server.tool"}}); err != nil {
		t.Fatal(err)
	}
	if got := rec.events[0].Item.Name; got != "mcp/server.tool" {
		t.Errorf("sent name = %q, want the client's", got)
	}

	if names := replyNames(req, &llm.Translation{To: llm.FormatGeminiGenerate, Passthrough: true}); names != nil {
		t.Errorf("passthrough names = %v, want none", names)
	}
}
