package openaichat

import (
	"encoding/json"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// A Chat request's allowed_tools choice is read into the model as a choice, not left in the
// request's own fields, and written back to Chat in the form the client sent, with the mode and
// the tools as it listed them. (A client also gets its body back unchanged through native
// passthrough; this is the translated path.)
func TestAllowedToolsParsedAndWrittenBack(t *testing.T) {
	in := `{"model":"m","stream":true,
	 "tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}},
	          {"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}],
	 "tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[
	   {"type":"function","function":{"name":"get_weather"}}]}},
	 "messages":[{"role":"user","content":"Weather?"}]}`
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
	want := `{"type":"allowed_tools","allowed_tools":{"mode":"auto","tools":[{"type":"function","function":{"name":"get_weather"}}]}}`
	if string(out.ToolChoice) != compactJSON(t, want) {
		t.Errorf("tool_choice = %s, want %s", out.ToolChoice, want)
	}
}
