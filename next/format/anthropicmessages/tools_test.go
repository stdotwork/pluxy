package anthropicmessages

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// Function names Messages does not take are sent under valid, distinct names, the same in the
// tool list, the replayed calls and the tool choice; ToolNames gives the client's name for each.
// The expected names are worked out by hand: characters replaced with underscores, and for two
// names that would be the same, the first ten hex digits of the SHA-256 of the client's name.
func TestToolNamesForMessages(t *testing.T) {
	long := strings.Repeat("x", 60) + ".tool"
	fn := func(name string) llm.Tool {
		return llm.Tool{Name: name, Parameters: json.RawMessage(`{"type":"object"}`)}
	}
	req := llm.Request{
		Model: "claude-test",
		Tools: []llm.Tool{fn("get_weather"), fn("mcp.fs.read"), fn("a.b"), fn("a:b"), fn(long)},
		Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}},
			{Type: llm.ItemTypeToolCall, CallID: "toolu_1", Name: "old.tool", Arguments: "{}"},
			{Type: llm.ItemTypeToolResult, CallID: "toolu_1", Output: "done"},
		},
		Options: llm.Options{ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: "mcp.fs.read"}},
	}
	want := map[string]string{
		"mcp.fs.read": "mcp_fs_read",
		"a.b":         "a_b_2e7336dc8e",
		"a:b":         "a_b_6783a31eab",
		long:          strings.Repeat("x", 60) + "_too",
		"old.tool":    "old_tool",
	}
	got := ToolNames(req)
	if len(got) != len(want) {
		t.Errorf("ToolNames = %v, want %v", got, want)
	}
	for from, to := range want {
		if got[from] != to {
			t.Errorf("ToolNames[%q] = %q, want %q", from, got[from], to)
		}
	}

	body, _, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice struct {
			Name string `json:"name"`
		} `json:"tool_choice"`
		Messages []struct {
			Content []struct {
				Name string `json:"name"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	wantNames := []string{"get_weather", "mcp_fs_read", "a_b_2e7336dc8e", "a_b_6783a31eab", strings.Repeat("x", 60) + "_too"}
	if strings.Join(names, " ") != strings.Join(wantNames, " ") {
		t.Errorf("tool names %v, want %v", names, wantNames)
	}
	if out.ToolChoice.Name != "mcp_fs_read" {
		t.Errorf("tool_choice name %q, want mcp_fs_read", out.ToolChoice.Name)
	}
	if len(out.Messages) < 2 || len(out.Messages[1].Content) == 0 || out.Messages[1].Content[0].Name != "old_tool" {
		t.Errorf("replayed call not renamed: %s", body)
	}
}

// A tool's parameter schema becomes an object schema without a root union, as Messages requires;
// the branches' properties and allOf's required names move to the root.
func TestInputSchemaForMessages(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"missing", ``, `{"type":"object","properties":{}}`},
		{"not an object", `[1]`, `{"type":"object","properties":{}}`},
		{"no properties", `{"type":"object"}`, `{"type":"object","properties":{}}`},
		{"anyOf", `{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"properties":{"b":{"type":"number"}}},{"type":"string"}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"number"}}}`},
		{"root property wins", `{"properties":{"a":{"type":"integer"}},"oneOf":[{"type":["object","null"],"properties":{"a":{"type":"string"},"c":{}}}]}`,
			`{"type":"object","properties":{"a":{"type":"integer"},"c":{}}}`},
		{"allOf required", `{"type":"object","required":["a"],"allOf":[{"properties":{"b":{}},"required":["b","a"]}]}`,
			`{"type":"object","required":["a","b"],"properties":{"b":{}}}`},
		{"anyOf required ignored", `{"anyOf":[{"properties":{"b":{}},"required":["b"]}]}`,
			`{"type":"object","properties":{"b":{}}}`},
		{"pattern kept", `{"type":"object","properties":{"n":{"type":"string","pattern":"^\\p{L}+$"}}}`,
			`{"type":"object","properties":{"n":{"type":"string","pattern":"^\\p{L}+$"}}}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := llm.Request{
				Model: "claude-test",
				Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}}},
				Tools: []llm.Tool{{Name: "f", Parameters: json.RawMessage(c.in)}},
			}
			body, _, err := EncodeRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Tools []struct {
					InputSchema json.RawMessage `json:"input_schema"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(body, &out); err != nil || len(out.Tools) != 1 {
				t.Fatalf("body %s: %v", body, err)
			}
			sameJSON(t, "input_schema", out.Tools[0].InputSchema, c.want)
		})
	}
}
