package main

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestIntegralArguments(t *testing.T) {
	for _, c := range []struct {
		name   string
		args   string
		schema string
		want   string
	}{
		{
			"integer",
			`{"max_tokens":2500.0}`,
			`{"type":"object","properties":{"max_tokens":{"type":"integer"}}}`,
			`{"max_tokens":2500}`,
		},
		{
			"nested",
			`{"opts":{"limit":3.0,"ratio":2.5},"keep":"2500.0"}`,
			`{"type":"object","properties":{"opts":{"type":"object","properties":{"limit":{"type":"integer"},"ratio":{"type":"number"}}}}}`,
			`{"opts":{"limit":3,"ratio":2.5},"keep":"2500.0"}`,
		},
		{
			"array items",
			`{"counts":[1.0,2.5,3e0]}`,
			`{"type":"object","properties":{"counts":{"type":"array","items":{"type":"integer"}}}}`,
			`{"counts":[1,2.5,3]}`,
		},
		{
			"nullable integer",
			`{"n":7.0,"none":null}`,
			`{"type":"object","properties":{"n":{"type":["integer","null"]},"none":{"type":["integer","null"]}}}`,
			`{"n":7,"none":null}`,
		},
		{
			"number is kept",
			`{"x":2500.0}`,
			`{"type":"object","properties":{"x":{"type":"number"}}}`,
			`{"x":2500.0}`,
		},
		{
			"unknown is kept",
			`{"x":2500.0}`,
			`{"type":"object"}`,
			`{"x":2500.0}`,
		},
		{
			"fraction is kept",
			`{"x":2500.5}`,
			`{"type":"object","properties":{"x":{"type":"integer"}}}`,
			`{"x":2500.5}`,
		},
		{
			"tiny fraction is kept",
			`{"x":2500.0000000000000001}`,
			`{"type":"object","properties":{"x":{"type":"integer"}}}`,
			`{"x":2500.0000000000000001}`,
		},
		{
			"beyond 2^53 is kept",
			`{"x":1e16}`,
			`{"type":"object","properties":{"x":{"type":"integer"}}}`,
			`{"x":1e16}`,
		},
		{
			"$ref is kept",
			`{"x":1.0}`,
			`{"type":"object","properties":{"x":{"$ref":"#/$defs/X"}},"$defs":{"X":{"type":"integer"}}}`,
			`{"x":1.0}`,
		},
		{
			"key order is kept",
			`{"b":1.0,"a":2.0,"s":"x","n":-4.0}`,
			`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"},"n":{"type":"integer"}}}`,
			`{"b":1,"a":2,"s":"x","n":-4}`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := integralArguments([]byte(c.args), json.RawMessage(c.schema))
			if string(got) != c.want {
				t.Errorf("integralArguments(%s) = %s, want %s", c.args, got, c.want)
			}
		})
	}
}

// A translated reply's call arguments are rewritten; a passthrough body's are not, and the call
// still comes back to the client under its own name.
func TestReportingRewritesTranslatedIntegralArguments(t *testing.T) {
	req := llm.Request{Tools: []llm.Tool{{
		Name:       "mcp/server.tool",
		Parameters: json.RawMessage(`{"type":"object","properties":{"timeout":{"type":"integer"}}}`),
	}}}
	tr := &llm.Translation{To: llm.FormatGeminiGenerate, From: llm.FormatAnthropicMessages}
	rec := &recorder{}
	r := &reporting{out: rec, tr: tr, names: replyNames(req, tr), schemas: toolSchemas(req), callNames: map[callKey]string{}}
	if err := r.Send(llm.Event{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{
		Kind: llm.ItemToolCall, CallID: "c1", Name: "mcp_server.tool"}}); err != nil {
		t.Fatal(err)
	}
	if got := rec.events[0].Item.Name; got != "mcp/server.tool" {
		t.Fatalf("name = %q, want the client's", got)
	}
	if err := r.Send(llm.Event{Type: llm.EventToolInputDelta, Index: 0, Delta: `{"timeout":2500.0}`}); err != nil {
		t.Fatal(err)
	}
	if got := rec.events[1].Delta; got != `{"timeout":2500}` {
		t.Errorf("delta = %s, want the integer", got)
	}

	// A passthrough body keeps its numbers and its names.
	pass := &reporting{out: rec, tr: &llm.Translation{To: llm.FormatGeminiGenerate, Passthrough: true}}
	if err := pass.Send(llm.Event{Type: llm.EventItemStart, Index: 1, Item: &llm.ItemStart{
		Kind: llm.ItemToolCall, CallID: "c2", Name: "f"}}); err != nil {
		t.Fatal(err)
	}
	if err := pass.Send(llm.Event{Type: llm.EventToolInputDelta, Index: 1, Delta: `{"timeout":2500.0}`}); err != nil {
		t.Fatal(err)
	}
	if got := rec.events[3].Delta; got != `{"timeout":2500.0}` {
		t.Errorf("passthrough delta = %s, want it as it came", got)
	}
}
