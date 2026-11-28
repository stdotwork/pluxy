package geminigenerate_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestCodeExecutionToolChoiceGemini(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-4-hydrogen",
		"input":"compute",
		"tool_choice":{"type":"code_interpreter"},
		"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{CodeExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []map[string]json.RawMessage `json:"tools"`
		ToolConfig json.RawMessage              `json:"toolConfig"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0]["codeExecution"] == nil {
		t.Fatalf("tools = %v, want codeExecution (body: %s)", got.Tools, body)
	}
	if len(got.ToolConfig) != 0 {
		t.Errorf("unexpected toolConfig = %s, want omitted", got.ToolConfig)
	}
	if !slices.Contains((&llm.Translation{Losses: losses}).DroppedPaths(), "tool_choice") {
		t.Errorf("dropped = %v, want tool_choice in losses %+v", (&llm.Translation{Losses: losses}).DroppedPaths(), losses)
	}

	ownReq, err := geminigenerate.ParseRequest("gemini-4-hydrogen", false, []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"codeExecution":{}}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyOwn, lossesOwn, err := geminigenerate.EncodeRequestWith(ownReq, geminigenerate.EncodeOptions{CodeExecution: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotOwn struct {
		ToolConfig struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	_ = json.Unmarshal(bodyOwn, &gotOwn)
	if gotOwn.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Errorf("own Gemini tool: functionCallingConfig.mode = %q, want ANY (body: %s)", gotOwn.ToolConfig.FunctionCallingConfig.Mode, bodyOwn)
	}
	if len(lossesOwn) != 0 {
		t.Errorf("own Gemini tool: unexpected losses %+v", lossesOwn)
	}
}

func TestCodeExecutionWithStructuredOutput(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-2.5-flash":      false,
		"gemini-2.5-pro":        false,
		"gemini-2.0-flash":      false,
		"models/gemini-2.5-pro": false,
		"gemini-3-flash":        true,
		"gemini-3.8-flash":      true,
		"gemini-4-neon":         true,
	} {
		t.Run(model, func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"compute",
				"text":{"format":{"type":"json_schema","name":"r","schema":{"type":"object"}}},
				"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{CodeExecution: true})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if sent := len(got.Tools) == 1 && got.Tools[0]["codeExecution"] != nil; sent != want {
				t.Errorf("codeExecution sent = %v, want %v (body: %s)", sent, want, body)
			}
			if lost := slices.Contains((&llm.Translation{Losses: losses}).DroppedPaths(), "tools[0]"); lost == want {
				t.Errorf("tools[0] dropped = %v with sent = %v: %+v", lost, want, losses)
			}
		})
	}
}
