package openaichat

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

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
