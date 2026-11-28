package geminigenerate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func TestToolNames(t *testing.T) {
	long := strings.Repeat("x", 140)
	req := llm.Request{
		Tools: []llm.Tool{
			{Name: "get_weather"},
			{Name: "mcp:server.tool"},
			{Name: "_private"},
			{Name: "weird/name"},
			{Name: "1starts-with-a-digit"},
			{Name: long},
			{Name: "a b"},
			{Name: "a@b"},
			{Name: "a_b"},
			{Type: llm.ToolNative},
		},
		Items: []llm.Item{
			{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "called/only"},
		},
	}
	names := ToolNames(req)
	if got := names["get_weather"]; got != "" {
		t.Errorf("valid name changed to %q", got)
	}
	if got := names["mcp:server.tool"]; got != "" {
		t.Errorf("valid name changed to %q", got)
	}
	if got := names["_private"]; got != "" {
		t.Errorf("valid name changed to %q", got)
	}
	if got := names["weird/name"]; got != "weird_name" {
		t.Errorf("weird/name -> %q, want weird_name", got)
	}
	if got := names["1starts-with-a-digit"]; got != "_1starts-with-a-digit" {
		t.Errorf("digit-led name -> %q, want an underscore in front", got)
	}
	if got := names[long]; len(got) != 128 || got[0] != 'x' {
		t.Errorf("long name -> %q (len %d), want 128 bytes", got, len(got))
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
		if len(to) > 128 {
			t.Errorf("%q -> %q is longer than 128 bytes", from, to)
		}
	}
	if !strings.HasPrefix(names["a b"], "a_b_") || !strings.HasPrefix(names["a@b"], "a_b_") {
		t.Errorf("colliding names not hashed: %q, %q", names["a b"], names["a@b"])
	}
	again := ToolNames(req)
	if len(again) != len(names) {
		t.Fatalf("ToolNames is not deterministic: %v vs %v", names, again)
	}
	for from, to := range names {
		if again[from] != to {
			t.Errorf("ToolNames(%q) changed between calls: %q vs %q", from, to, again[from])
		}
	}
}

func encodeGemini(t *testing.T, req llm.Request) (body []byte, decls []string, contents []map[string]any, config map[string]any) {
	t.Helper()
	body, _, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		Contents   []map[string]any `json:"contents"`
		ToolConfig struct {
			FunctionCallingConfig map[string]any `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	for _, t := range out.Tools {
		for _, fd := range t.FunctionDeclarations {
			decls = append(decls, fd.Name)
		}
	}
	return body, decls, out.Contents, out.ToolConfig.FunctionCallingConfig
}

func TestEncodeRequestUsesToolNames(t *testing.T) {
	req := llm.Request{Model: "m",
		Tools: []llm.Tool{{Name: "weird/name", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Items: []llm.Item{
			{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "weird/name", Arguments: `{"x":1}`},
			{Type: llm.ItemTypeToolResult, CallID: "c1", Output: "ok"},
		},
		Options: llm.Options{ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: "weird/name"}},
	}
	body, decls, contents, config := encodeGemini(t, req)
	if len(decls) != 1 || decls[0] != "weird_name" {
		t.Errorf("declarations = %v, want weird_name", decls)
	}
	if len(contents) != 2 {
		t.Fatalf("contents = %v, want a model turn and a user turn", contents)
	}
	parts, _ := contents[0]["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("model parts = %v", parts)
	}
	if call, ok := parts[0].(map[string]any)["functionCall"].(map[string]any); !ok || call["name"] != "weird_name" {
		t.Errorf("functionCall = %v, want name weird_name", parts[0])
	}
	userParts, _ := contents[1]["parts"].([]any)
	if len(userParts) != 1 {
		t.Fatalf("user parts = %v", userParts)
	}
	if resp, ok := userParts[0].(map[string]any)["functionResponse"].(map[string]any); !ok || resp["name"] != "weird_name" {
		t.Errorf("functionResponse = %v, want name weird_name", userParts[0])
	}
	if got := config["allowedFunctionNames"]; got == nil || len(got.([]any)) != 1 || got.([]any)[0] != "weird_name" {
		t.Errorf("allowedFunctionNames = %v, want [weird_name]; body %s", got, body)
	}
}

func TestEncodeRequestKeepsValidToolNames(t *testing.T) {
	req := llm.Request{Model: "m",
		Tools: []llm.Tool{{Name: "get_weather"}, {Name: "mcp:server.tool"}},
		Items: []llm.Item{{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "get_weather", Arguments: "{}"}},
	}
	_, decls, _, _ := encodeGemini(t, req)
	if strings.Join(decls, ",") != "get_weather,mcp:server.tool" {
		t.Errorf("declarations = %v, want the names as sent", decls)
	}
}
