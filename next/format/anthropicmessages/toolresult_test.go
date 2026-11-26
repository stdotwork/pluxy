package anthropicmessages

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A tool result that holds a screenshot and a PDF besides its text, and says the tool failed, as
// a computer-use or browser tool returns one.
const richToolResult = `{"model":"m","max_tokens":100,
"tools":[{"name":"screenshot","input_schema":{"type":"object"}}],
"messages":[
 {"role":"user","content":"Take a screenshot."},
 {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"screenshot","input":{}}]},
 {"role":"user","content":[
  {"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":[
   {"type":"text","text":"Saved, with warnings."},
   {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
   {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}]},
  {"type":"text","text":"What do you see?"}]}
]}`

// messagesAt returns the list under key in an encoded body.
func listAt(t *testing.T, body []byte, key string) []json.RawMessage {
	t.Helper()
	var list []json.RawMessage
	if err := json.Unmarshal(fieldOf(t, body, key), &list); err != nil {
		t.Fatalf("%s: %v: %s", key, err, body)
	}
	return list
}

// A tool result's images, documents and error flag are read into the model: the parts in order,
// the text joined in Output for encoders that can send only text.
func TestParseRichToolResult(t *testing.T) {
	req, err := ParseRequest([]byte(richToolResult))
	if err != nil {
		t.Fatal(err)
	}
	var it llm.Item
	for _, x := range req.Items {
		if x.Type == llm.ItemTypeToolResult {
			it = x
		}
	}
	if it.CallID != "toolu_1" || it.Output != "Saved, with warnings." || !it.IsError {
		t.Fatalf("tool result = call %q, output %q, is_error %v; want toolu_1, the text, true", it.CallID, it.Output, it.IsError)
	}
	var kinds []string
	for _, p := range it.Content {
		kinds = append(kinds, p.Type+" "+p.Text+p.URL+p.FileData)
	}
	want := []string{"text Saved, with warnings.", "image data:image/png;base64,iVBORw0KGgo=", "file data:application/pdf;base64,JVBERi0="}
	if !reflect.DeepEqual(kinds, want) {
		t.Errorf("parts = %q, want %q", kinds, want)
	}
	if it.Origin != nil && len(it.Origin.Raw) > 0 {
		t.Errorf("origin raw = %s, want nothing left unmodelled", it.Origin.Raw)
	}
}

// Rebuilt as Messages, the result keeps its blocks and its error flag.
func TestRichToolResultToMessages(t *testing.T) {
	req, err := ParseRequest([]byte(richToolResult))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	msgs := listAt(t, body, "messages")
	sameJSON(t, "tool result turn", msgs[2], `{"role":"user","content":[
	 {"type":"tool_result","tool_use_id":"toolu_1","is_error":true,"content":[
	  {"type":"text","text":"Saved, with warnings."},
	  {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}},
	  {"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}]},
	 {"type":"text","text":"What do you see?"}]}`)
	if len(losses) > 0 {
		t.Errorf("losses = %v, want none", lossList(losses))
	}
}

// Chat Completions takes only text in a tool message, so the images and files follow in the user
// message after the tool messages, introduced by a line that says where they came from (as
// CLIProxyAPI relays them). The error flag has no place.
func TestRichToolResultToChat(t *testing.T) {
	req, err := ParseRequest([]byte(richToolResult))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	msgs := listAt(t, body, "messages")
	if len(msgs) != 4 {
		t.Fatalf("%d messages, want 4: %s", len(msgs), body)
	}
	sameJSON(t, "tool message", msgs[2], `{"role":"tool","tool_call_id":"toolu_1","content":"Saved, with warnings."}`)
	sameJSON(t, "user message", msgs[3], `{"role":"user","content":[
	 {"type":"text","text":"Images and files returned by the preceding tool call(s):"},
	 {"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}},
	 {"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0="}},
	 {"type":"text","text":"What do you see?"}]}`)
	want := []string{
		"approximated messages[2].content[0].content[1]",
		"approximated messages[2].content[0].content[2]",
		"dropped messages[2].content[0].is_error",
	}
	if got := lossList(losses); !reflect.DeepEqual(got, want) {
		t.Errorf("losses = %q, want %q", got, want)
	}
}

// Responses takes the parts in function_call_output's output list.
func TestRichToolResultToResponses(t *testing.T) {
	req, err := ParseRequest([]byte(richToolResult))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openairesponses.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	input := listAt(t, body, "input")
	if len(input) != 4 {
		t.Fatalf("%d input items, want 4: %s", len(input), body)
	}
	sameJSON(t, "function_call_output", input[2], `{"type":"function_call_output","call_id":"toolu_1","output":[
	 {"type":"input_text","text":"Saved, with warnings."},
	 {"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo=","detail":"auto"},
	 {"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0="}]}`)
	want := []string{"dropped messages[2].content[0].is_error"}
	if got := lossList(losses); !reflect.DeepEqual(got, want) {
		t.Errorf("losses = %q, want %q", got, want)
	}
}

// GenerateContent takes the media in functionResponse.parts and a failure under the response's
// error key.
func TestRichToolResultToGemini(t *testing.T) {
	req, err := ParseRequest([]byte(richToolResult))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := geminigenerate.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	contents := listAt(t, body, "contents")
	if len(contents) != 3 {
		t.Fatalf("%d contents, want 3: %s", len(contents), body)
	}
	sameJSON(t, "function response turn", contents[2], `{"role":"user","parts":[
	 {"functionResponse":{"id":"toolu_1","name":"screenshot","response":{"error":"Saved, with warnings."},"parts":[
	  {"inlineData":{"mimeType":"image/png","data":"iVBORw0KGgo="}},
	  {"inlineData":{"mimeType":"application/pdf","data":"JVBERi0="}}]}},
	 {"text":"What do you see?"}]}`)
	if len(losses) > 0 {
		t.Errorf("losses = %v, want none", lossList(losses))
	}
}

// An image a Chat client put in a tool message reaches Messages inside the tool_result.
func TestChatToolImageToMessages(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"m","messages":[
	 {"role":"user","content":"Take a screenshot."},
	 {"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"screenshot","arguments":"{}"}}]},
	 {"role":"tool","tool_call_id":"call_1","content":[
	  {"type":"text","text":"Saved."},
	  {"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	msgs := listAt(t, body, "messages")
	sameJSON(t, "tool result turn", msgs[2], `{"role":"user","content":[
	 {"type":"tool_result","tool_use_id":"call_1","content":[
	  {"type":"text","text":"Saved."},
	  {"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]}]}`)
	if len(losses) > 0 {
		t.Errorf("losses = %v, want none", lossList(losses))
	}
}
