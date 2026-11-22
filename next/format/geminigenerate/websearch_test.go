package geminigenerate_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestWebSearchToolChoiceGemini(t *testing.T) {
	// Responses request with tool_choice: "required" and only web_search must NOT send
	// toolConfig.functionCallingConfig and MUST report a dropped loss at tool_choice.
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-2.5-flash",
		"input":"latest news",
		"tool_choice":"required",
		"tools":[{"type":"web_search"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{WebSearch: true})
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
	if len(got.Tools) != 1 || got.Tools[0]["googleSearch"] == nil {
		t.Fatalf("tools = %v, want googleSearch (body: %s)", got.Tools, body)
	}
	if len(got.ToolConfig) != 0 {
		t.Errorf("unexpected toolConfig = %s, want omitted", got.ToolConfig)
	}
	tr := &llm.Translation{Losses: losses}
	if !slices.Contains(tr.DroppedPaths(), "tool_choice") {
		t.Errorf("dropped = %v, want tool_choice in losses %+v", tr.DroppedPaths(), losses)
	}

	// Named tool choice with only translated web_search: no toolConfig, dropped loss at tool_choice.
	reqNamed, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-2.5-flash",
		"input":"latest news",
		"tool_choice":{"type":"function","name":"get_weather"},
		"tools":[{"type":"web_search"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyNamed, lossesNamed, err := geminigenerate.EncodeRequestWith(reqNamed, geminigenerate.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotNamed struct {
		ToolConfig json.RawMessage `json:"toolConfig"`
	}
	_ = json.Unmarshal(bodyNamed, &gotNamed)
	if len(gotNamed.ToolConfig) != 0 {
		t.Errorf("named choice: unexpected toolConfig = %s", gotNamed.ToolConfig)
	}
	if !slices.Contains((&llm.Translation{Losses: lossesNamed}).DroppedPaths(), "tool_choice") {
		t.Errorf("named choice: dropped = %v, want tool_choice", (&llm.Translation{Losses: lossesNamed}).DroppedPaths())
	}

	// tool_choice: "auto" with only translated web_search: no toolConfig, no loss.
	reqAuto, err := openairesponses.ParseRequest([]byte(`{
		"model":"gemini-2.5-flash",
		"input":"latest news",
		"tool_choice":"auto",
		"tools":[{"type":"web_search"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyAuto, lossesAuto, err := geminigenerate.EncodeRequestWith(reqAuto, geminigenerate.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotAuto struct {
		ToolConfig json.RawMessage `json:"toolConfig"`
	}
	_ = json.Unmarshal(bodyAuto, &gotAuto)
	if len(gotAuto.ToolConfig) != 0 {
		t.Errorf("auto choice: unexpected toolConfig = %s", gotAuto.ToolConfig)
	}
	if len(lossesAuto) != 0 {
		t.Errorf("auto choice: unexpected losses %+v", lossesAuto)
	}

	// Gemini client's own native tools keep today's behaviour (toolConfig preserved).
	ownReq, err := geminigenerate.ParseRequest("gemini-2.5-flash", false, []byte(`{
		"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		"tools":[{"googleSearch":{}}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY"}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyOwn, lossesOwn, err := geminigenerate.EncodeRequestWith(ownReq, geminigenerate.EncodeOptions{WebSearch: true})
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
