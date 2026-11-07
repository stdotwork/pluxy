package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func lossPaths(c *chatRequest) []string {
	var p []string
	for _, l := range c.losses {
		if l.Kind == llm.LossDropped {
			p = append(p, l.Path)
		}
	}
	return p
}

func userText(s string) llm.Item {
	return llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: s}}}
}

// What Devin has no place for is listed as dropped: a client's fields kept beside the model,
// parallel tool calls turned off and strict tool arguments. A replayed signature, which Devin
// takes, is not.
func TestDroppedFields(t *testing.T) {
	no, yes := false, true
	req := &llm.Request{Model: "swe-2", Options: llm.Options{ParallelToolCalls: &no},
		Origin: &llm.Origin{Format: llm.FormatOpenAIChat, Raw: json.RawMessage(`{"seed":7}`)},
		Tools:  []llm.Tool{{Type: llm.ToolFunction, Name: "f", Strict: &yes, Origin: &llm.Origin{Format: llm.FormatOpenAIChat, Path: "tools[0]"}}},
		Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}},
				Origin: &llm.Origin{Format: llm.FormatOpenAIChat, Path: "messages[0]", Raw: json.RawMessage(`{"name":"bob"}`)}},
			{Type: llm.ItemTypeReasoning, Text: "t", Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "messages[1].content[0]", Raw: json.RawMessage(`{"signature":"sealed.v1.x"}`)}},
		}}
	c, err := buildRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	got := lossPaths(c)
	for _, want := range []string{"seed", "parallel_tool_calls", "tools[0].strict", "messages[0].name"} {
		if !slices.Contains(got, want) {
			t.Errorf("dropped %v, missing %s", got, want)
		}
	}
	if slices.Contains(got, "messages[1].content[0].signature") {
		t.Errorf("dropped %v lists the replayed signature", got)
	}
	srv := devintest.New(t)
	req.DropPolicy = llm.DropReject
	err = chat(context.Background(), sdk.Call{Account: protocol.Account{Settings: srv.Settings(), Credential: tokenCredential(srv.Token())}, Request: *req}, &recorder{})
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || len(srv.Requests()) != 0 {
		t.Errorf("reject policy: err %v, %d requests sent; want unsupported and none", err, len(srv.Requests()))
	}
}

// A tool result given as content blocks keeps its text; its images, which Devin cannot take in a
// result, are listed as dropped.
func TestMixedToolResult(t *testing.T) {
	for _, c := range []struct {
		name, format, raw, dropped string
	}{
		{"messages", llm.FormatAnthropicMessages, `{"content":[{"type":"text","text":"The temperature is 18"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}}]}`, "r.content[1]"},
		{"responses", llm.FormatOpenAIResponses, `{"output":[{"type":"input_text","text":"The temperature is 18"},{"type":"input_image","image_url":"https://example.com/a.png"}]}`, "r.output[1]"},
	} {
		req := &llm.Request{Model: "swe-2", Items: []llm.Item{
			userText("Weather?"),
			{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "weather", Arguments: `{}`},
			{Type: llm.ItemTypeToolResult, CallID: "call_1", Origin: &llm.Origin{Format: c.format, Path: "r", Raw: json.RawMessage(c.raw)}},
		}}
		cr, err := buildRequest(req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := cr.turns[2].content; got != "The temperature is 18" {
			t.Errorf("%s: tool turn text %q", c.name, got)
		}
		if got := lossPaths(cr); !slices.Contains(got, c.dropped) {
			t.Errorf("%s: dropped %v, want %s", c.name, got, c.dropped)
		}
	}
}

// A user turn left with nothing once media Devin cannot take is dropped is refused, as today's
// core refuses it; a turn that keeps other content loses only the emptied message.
func TestEmptiedUserTurn(t *testing.T) {
	remote := llm.Item{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartImage, URL: "https://example.com/a.png"}}}
	_, err := buildRequest(&llm.Request{Model: "swe-2", Items: []llm.Item{remote}})
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeRequest {
		t.Errorf("image-only turn: err %v, want unsupported on the request", err)
	}
	srv := devintest.New(t)
	err = chat(context.Background(), sdk.Call{Account: protocol.Account{Settings: srv.Settings(), Credential: tokenCredential(srv.Token())},
		Request: llm.Request{Model: "swe-2", Items: []llm.Item{remote}}}, &recorder{})
	if !errors.As(err, &e) || e.Code != llm.CodeUnsupported || len(srv.Requests()) != 0 {
		t.Errorf("image-only turn sent: err %v, %d requests; want unsupported and none", err, len(srv.Requests()))
	}
	c, err := buildRequest(&llm.Request{Model: "swe-2", Items: []llm.Item{remote, userText("What is this?")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.turns) != 1 || c.turns[0].content != "What is this?" {
		t.Errorf("turns %+v, want only the text", c.turns)
	}
}
