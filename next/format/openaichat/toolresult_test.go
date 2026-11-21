package openaichat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// An image in a tool message is read as a part of the result, with the text joined in Output.
func TestParseToolMessageImage(t *testing.T) {
	req, err := ParseRequest([]byte(`{"model":"m","messages":[
	 {"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
	 {"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"Saved."},{"type":"image_url","image_url":{"url":"https://x.test/a.png","detail":"low"}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	it := req.Items[len(req.Items)-1]
	if it.Type != llm.ItemTypeToolResult || it.Output != "Saved." || len(it.Content) != 2 {
		t.Fatalf("item = %+v, want a tool result with output Saved. and 2 parts", it)
	}
	if p := it.Content[1]; p.Type != llm.PartImage || p.URL != "https://x.test/a.png" || p.Detail != "low" {
		t.Errorf("part 1 = %+v, want the image", p)
	}
}

// Images and files from several tool results go together in one user message after the last
// tool message, so every tool message still follows the call message directly; a user message
// that follows takes them at its start.
func TestToolResultMediaFollowToolMessages(t *testing.T) {
	img := llm.Part{Type: llm.PartImage, URL: "data:image/png;base64,AAAA"}
	file := llm.Part{Type: llm.PartFile, FileID: "file_1"}
	req := llm.Request{Model: "m", Items: []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "go"}}, TextContent: true},
		{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "f", Arguments: "{}"},
		{Type: llm.ItemTypeToolCall, CallID: "c2", Name: "g", Arguments: "{}"},
		{Type: llm.ItemTypeToolResult, CallID: "c1", Content: []llm.Part{img}},
		{Type: llm.ItemTypeToolResult, CallID: "c2", Output: "done", Content: []llm.Part{{Type: llm.PartText, Text: "done"}, file}},
		{Type: llm.ItemTypeToolCall, CallID: "c3", Name: "f", Arguments: "{}"},
		{Type: llm.ItemTypeToolResult, CallID: "c3", Content: []llm.Part{img}},
	}}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Messages []json.RawMessage }
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range out.Messages {
		got = append(got, string(m))
	}
	want := []string{
		`{"role":"user","content":"go"}`,
		`{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"g","arguments":"{}"}}]}`,
		`{"role":"tool","content":"[The tool returned images; they follow in the next user message.]","tool_call_id":"c1"}`,
		`{"role":"tool","content":"done","tool_call_id":"c2"}`,
		`{"role":"user","content":[{"type":"text","text":"Images and files returned by the preceding tool call(s):"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"file","file":{"file_id":"file_1"}}]}`,
		`{"role":"assistant","tool_calls":[{"id":"c3","type":"function","function":{"name":"f","arguments":"{}"}}]}`,
		`{"role":"tool","content":"[The tool returned images; they follow in the next user message.]","tool_call_id":"c3"}`,
		`{"role":"user","content":[{"type":"text","text":"Images returned by the preceding tool call(s):"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("messages:\n got %q\nwant %q", got, want)
	}
	if len(losses) != 3 {
		t.Errorf("losses = %+v, want one approximation per image or file", losses)
	}
}
