package openairesponses

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A Responses request's allowed_tools choice is read into the model as a choice, not left in the
// request's own fields, and written back to Responses in the form the client sent. (A client also
// gets its body back unchanged through native passthrough; this is the translated path.)
func TestAllowedToolsParsedAndWrittenBack(t *testing.T) {
	in := `{"model":"m","stream":true,
	 "tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}},
	          {"type":"function","name":"get_time","parameters":{"type":"object"}}],
	 "tool_choice":{"type":"allowed_tools","mode":"auto","tools":[
	   {"type":"function","name":"get_weather"}]},
	 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather?"}]}]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if req.Options.ToolChoice == nil || req.Options.ToolChoice.Mode != llm.ToolChoiceAuto {
		t.Fatalf("ToolChoice = %+v, want the allowed_tools choice read as auto", req.Options.ToolChoice)
	}
	body, _, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ToolChoice json.RawMessage `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"get_weather"}]}`
	var got, w any
	if err := json.Unmarshal(out.ToolChoice, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, w) {
		t.Errorf("tool_choice = %s, want %s", out.ToolChoice, want)
	}
}

// A tool_choice allowed_tools naming a hosted tool as well as functions stays in the request's own
// fields: the model carries function names, so the hosted entry keeps the whole choice as it was.
func TestAllowedToolsWithHostedToolStaysNative(t *testing.T) {
	in := `{"model":"m","stream":true,
	 "tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}}],
	 "tool_choice":{"type":"allowed_tools","mode":"auto","tools":[
	   {"type":"function","name":"get_weather"},{"type":"mcp","server_label":"deepwiki"}]},
	 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather?"}]}]}`
	req, err := ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if req.Options.ToolChoice != nil {
		t.Fatalf("ToolChoice = %+v, want the choice left in the request's own fields", req.Options.ToolChoice)
	}
	if !strings.Contains(string(req.Origin.Raw), `"allowed_tools"`) {
		t.Errorf("Origin.Raw = %s, want the allowed_tools choice kept", req.Origin.Raw)
	}
}
