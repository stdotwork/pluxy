package host

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestCodeExecutionDowngradeTools(t *testing.T) {
	orig := &llm.Origin{
		Format: llm.FormatOpenAIResponses,
		Path:   "tools[0]",
		Raw:    json.RawMessage(`{"type":"code_interpreter","container":{"type":"auto"}}`),
	}
	req := llm.Request{
		Model: "claude-sonnet-4-6",
		Tools: []llm.Tool{
			{Type: llm.ToolCodeExecution, Origin: orig},
			{Type: llm.ToolFunction, Name: "get_weather"},
		},
	}

	got29 := downgradeCodeExecution(29, req)
	if len(got29.Tools) != 2 {
		t.Fatalf("minor 29: len(Tools) = %d, want 2", len(got29.Tools))
	}
	if got29.Tools[0].Type != llm.ToolNative {
		t.Errorf("minor 29: Tools[0].Type = %q, want %q", got29.Tools[0].Type, llm.ToolNative)
	}
	if !reflect.DeepEqual(got29.Tools[0].Origin, orig) {
		t.Errorf("minor 29: Tools[0].Origin = %+v, want %+v", got29.Tools[0].Origin, orig)
	}
	if got29.Tools[1].Name != "get_weather" {
		t.Errorf("minor 29: Tools[1] changed: %+v", got29.Tools[1])
	}
	if req.Tools[0].Type != llm.ToolCodeExecution {
		t.Errorf("original req mutated: Tools[0].Type = %q", req.Tools[0].Type)
	}

	got30 := downgradeCodeExecution(30, req)
	if !reflect.DeepEqual(got30, req) {
		t.Errorf("minor 30: req changed: got %+v, want %+v", got30, req)
	}
}

func TestCodeExecutionDowngradeResponsesTopLevelToolChoice(t *testing.T) {
	raw := []byte(`{"model":"gpt-5.4","input":"compute 2+2","metadata":{"k":"v"},"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],"tool_choice":{"type":"code_interpreter"}}`)
	req, err := openairesponses.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Options.ToolChoice == nil || req.Options.ToolChoice.Mode != llm.ToolChoiceCodeExecution {
		t.Fatalf("parsed ToolChoice = %+v, want mode %q", req.Options.ToolChoice, llm.ToolChoiceCodeExecution)
	}
	origPtr := req.Origin
	origRawBefore := append([]byte(nil), req.Origin.Raw...)

	got := downgradeCodeExecution(29, req)
	if got.Options.ToolChoice != nil {
		t.Errorf("minor 29: ToolChoice = %+v, want nil", got.Options.ToolChoice)
	}
	if got.Tools[0].Type != llm.ToolNative {
		t.Errorf("minor 29: Tools[0].Type = %q, want native", got.Tools[0].Type)
	}
	if got.Origin == origPtr {
		t.Errorf("minor 29: mutated caller Origin pointer")
	}
	if !bytes.Equal(origPtr.Raw, origRawBefore) {
		t.Errorf("minor 29: mutated caller Origin.Raw: got %s, want %s", origPtr.Raw, origRawBefore)
	}
	body, losses, err := openairesponses.EncodeRequest(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("unexpected losses: %+v", losses)
	}
	want := `{"input":[{"content":[{"text":"compute 2+2","type":"input_text"}],"role":"user","type":"message"}],"metadata":{"k":"v"},"model":"gpt-5.4","stream":false,"tool_choice":{"type":"code_interpreter"},"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`
	if string(body) != want {
		t.Errorf("encoded body:\ngot:  %s\nwant: %s", body, want)
	}

	rawBare := []byte(`{"model":"gpt-5.4","input":"compute 2+2","tools":[{"type":"code_interpreter","container":{"type":"auto"}}],"tool_choice":{"type":"code_interpreter"}}`)
	reqBare, err := openairesponses.ParseRequest(rawBare)
	if err != nil {
		t.Fatal(err)
	}
	if reqBare.Origin != nil && len(reqBare.Origin.Raw) != 0 {
		t.Fatalf("expected empty Origin.Raw on bare Responses request, got %s", reqBare.Origin.Raw)
	}
	gotBare := downgradeCodeExecution(29, reqBare)
	if gotBare.Options.ToolChoice != nil {
		t.Errorf("bare minor 29: ToolChoice = %+v, want nil", gotBare.Options.ToolChoice)
	}
	bodyBare, lossesBare, err := openairesponses.EncodeRequest(gotBare)
	if err != nil {
		t.Fatal(err)
	}
	if len(lossesBare) != 0 {
		t.Errorf("bare unexpected losses: %+v", lossesBare)
	}
	wantBare := `{"input":[{"content":[{"text":"compute 2+2","type":"input_text"}],"role":"user","type":"message"}],"model":"gpt-5.4","stream":false,"tool_choice":{"type":"code_interpreter"},"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`
	if string(bodyBare) != wantBare {
		t.Errorf("bare encoded body:\ngot:  %s\nwant: %s", bodyBare, wantBare)
	}
}

func TestCodeExecutionDowngradeMessagesToolChoice(t *testing.T) {
	raw := []byte(`{"model":"claude-sonnet-4-6","max_tokens":1024,"messages":[{"role":"user","content":"compute 2+2"}],"tools":[{"type":"code_execution_20250825","name":"code_execution"}],"tool_choice":{"type":"tool","name":"code_execution"}}`)
	req, err := anthropicmessages.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.Options.ToolChoice == nil || req.Options.ToolChoice.Mode != llm.ToolChoiceCodeExecution {
		t.Fatalf("parsed ToolChoice = %+v, want mode %q", req.Options.ToolChoice, llm.ToolChoiceCodeExecution)
	}
	origChoicePtr := req.Options.ToolChoice

	got := downgradeCodeExecution(29, req)
	if got.Tools[0].Type != llm.ToolNative {
		t.Errorf("minor 29: Tools[0].Type = %q, want native", got.Tools[0].Type)
	}
	if got.Options.ToolChoice == nil || got.Options.ToolChoice.Mode != llm.ToolChoiceTool || got.Options.ToolChoice.Name != "code_execution" {
		t.Errorf("minor 29: ToolChoice = %+v, want {Mode:tool Name:code_execution}", got.Options.ToolChoice)
	}
	if got.Options.ToolChoice == origChoicePtr || origChoicePtr.Mode != llm.ToolChoiceCodeExecution {
		t.Errorf("minor 29: mutated caller ToolChoice")
	}
	body, losses, err := anthropicmessages.EncodeRequest(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("unexpected losses: %+v", losses)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	tc, _ := m["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "code_execution" {
		t.Errorf("encoded tool_choice = %v, want {type:tool name:code_execution}", tc)
	}
}

func TestCodeExecutionDowngradeGemini(t *testing.T) {
	raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"compute"}]}],"tools":[{"codeExecution":{}}]}`)
	req, err := geminigenerate.ParseRequest("gemini-4-hydrogen", false, raw)
	if err != nil {
		t.Fatal(err)
	}
	got := downgradeCodeExecution(29, req)
	if len(got.Tools) != 1 || got.Tools[0].Type != llm.ToolNative {
		t.Fatalf("minor 29: tools = %+v, want single ToolNative", got.Tools)
	}
	body, losses, err := geminigenerate.EncodeRequest(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("unexpected losses: %+v", losses)
	}
	var m struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &m)
	if len(m.Tools) != 1 || m.Tools[0]["codeExecution"] == nil {
		t.Errorf("encoded tools = %v, want codeExecution", m.Tools)
	}
}

func TestAllowedToolsDowngradeExcludesCodeExecution(t *testing.T) {
	req := llm.Request{
		Model: "gpt-5.4",
		Tools: []llm.Tool{
			{Type: llm.ToolCodeExecution, Name: "code_execution"},
			{Type: llm.ToolFunction, Name: "get_weather"},
		},
		Options: llm.Options{
			ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceAuto, Allowed: []string{"code_execution"}},
		},
	}
	if _, err := downgradeAllowedTools(25, req); err == nil {
		t.Fatalf("expected invalid_request when allowed_tools names only a ToolCodeExecution, got nil")
	}
}
