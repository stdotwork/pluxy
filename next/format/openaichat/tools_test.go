package openaichat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// Chat Completions takes 1 to 64 characters from a-z, A-Z, 0-9, _ and -; other names are sent
// under a name it takes.
func TestToolNames(t *testing.T) {
	long := strings.Repeat("x", 70)
	req := llm.Request{
		Tools: []llm.Tool{
			{Name: "get_weather"},
			{Name: "_private"},
			{Name: "mcp:server.tool"},
			{Name: "weird/name"},
			{Name: long},
			{Name: "a b"},
			{Name: "a@b"},
			{Name: "a_b"},
			{Type: llm.ToolNative},
		},
		Items: []llm.Item{{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "called/only"}},
	}
	names := ToolNames(req)
	if got := names["get_weather"]; got != "" {
		t.Errorf("valid name changed to %q", got)
	}
	if got := names["_private"]; got != "" {
		t.Errorf("valid name changed to %q", got)
	}
	if got := names["mcp:server.tool"]; got != "mcp_server_tool" {
		t.Errorf("mcp:server.tool -> %q, want mcp_server_tool", got)
	}
	if got := names["weird/name"]; got != "weird_name" {
		t.Errorf("weird/name -> %q, want weird_name", got)
	}
	if got := names[long]; len(got) != 64 || got[0] != 'x' {
		t.Errorf("long name -> %q (len %d), want 64 bytes", got, len(got))
	}
	if got := names["called/only"]; got != "called_only" {
		t.Errorf("called/only -> %q, want called_only", got)
	}
	seen := map[string]bool{}
	for _, from := range []string{"a b", "a@b", "a_b"} {
		to := from
		if changed := names[from]; changed != "" {
			to = changed
		}
		if seen[to] {
			t.Errorf("%q and another name both map to %q", from, to)
		}
		seen[to] = true
		if len(to) > 64 {
			t.Errorf("%q -> %q is longer than 64 bytes", from, to)
		}
	}
	if !strings.HasPrefix(names["a b"], "a_b_") || !strings.HasPrefix(names["a@b"], "a_b_") {
		t.Errorf("colliding names not hashed: %q, %q", names["a b"], names["a@b"])
	}
}

// encodeChatDecoded decodes the body EncodeRequest built.
func encodeChatDecoded(t *testing.T, req llm.Request) (decls []string, calls []string, choice any) {
	t.Helper()
	body, _, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
		ToolChoice any `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	for _, t := range out.Tools {
		decls = append(decls, t.Function.Name)
	}
	for _, m := range out.Messages {
		for _, c := range m.ToolCalls {
			calls = append(calls, c.Function.Name)
		}
	}
	return decls, calls, out.ToolChoice
}

// The declarations and the replayed calls use the same Chat name, and a tool choice names it too.
func TestEncodeRequestUsesToolNames(t *testing.T) {
	req := llm.Request{Model: "m",
		Tools: []llm.Tool{{Name: "mcp:server.tool", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Items: []llm.Item{
			{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "mcp:server.tool", Arguments: "{}"},
			{Type: llm.ItemTypeToolResult, CallID: "c1", Output: "ok"},
		},
		Options: llm.Options{ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: "mcp:server.tool"}},
	}
	decls, calls, choice := encodeChatDecoded(t, req)
	if len(decls) != 1 || decls[0] != "mcp_server_tool" {
		t.Errorf("declarations = %v, want mcp_server_tool", decls)
	}
	if len(calls) != 1 || calls[0] != "mcp_server_tool" {
		t.Errorf("replayed calls = %v, want mcp_server_tool", calls)
	}
	got, _ := choice.(map[string]any)
	if fn, _ := got["function"].(map[string]any); fn["name"] != "mcp_server_tool" {
		t.Errorf("tool_choice = %v, want the Chat name", choice)
	}
}

// A request whose names are already valid is sent unchanged, and its tool choice too.
func TestEncodeRequestKeepsValidToolNames(t *testing.T) {
	req := llm.Request{Model: "m",
		Tools:   []llm.Tool{{Name: "get_weather"}, {Name: "_private"}},
		Items:   []llm.Item{{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "get_weather", Arguments: "{}"}},
		Options: llm.Options{ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: "get_weather"}},
	}
	decls, calls, choice := encodeChatDecoded(t, req)
	if strings.Join(decls, ",") != "get_weather,_private" {
		t.Errorf("declarations = %v, want the names as sent", decls)
	}
	if len(calls) != 1 || calls[0] != "get_weather" {
		t.Errorf("replayed calls = %v", calls)
	}
	if got, _ := choice.(map[string]any); got["type"] != "function" {
		t.Errorf("tool_choice changed: %v", choice)
	}
}
