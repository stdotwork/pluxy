package openaichat_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestCodeExecutionDroppedInChat(t *testing.T) {
	// Responses code_interpreter with forced tool_choice -> both dropped with losses, no tool_choice emitted.
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"input":"compute",
		"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],
		"tool_choice":{"type":"code_interpreter"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := openaichat.EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice any               `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 0 || got.ToolChoice != nil {
		t.Errorf("got tools=%v tool_choice=%v, want both omitted (body: %s)", got.Tools, got.ToolChoice, body)
	}
	dropped := (&llm.Translation{Losses: losses}).DroppedPaths()
	if !slices.Contains(dropped, "tools[0]") || !slices.Contains(dropped, "tool_choice") {
		t.Errorf("dropped = %v, want tools[0] and tool_choice in %+v", dropped, losses)
	}

	// Messages code_execution beside a function tool, forcing code_execution -> function kept, tool_choice dropped.
	reqMsg, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"compute"}],
		"tools":[
			{"name":"get_weather","input_schema":{"type":"object"}},
			{"type":"code_execution_20250825","name":"code_execution"}
		],
		"tool_choice":{"type":"tool","name":"code_execution"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	bodyMsg, lossesMsg, err := openaichat.EncodeRequest(reqMsg)
	if err != nil {
		t.Fatal(err)
	}
	var gotMsg struct {
		Tools      []json.RawMessage `json:"tools"`
		ToolChoice any               `json:"tool_choice"`
	}
	_ = json.Unmarshal(bodyMsg, &gotMsg)
	if len(gotMsg.Tools) != 1 || gotMsg.ToolChoice != nil {
		t.Errorf("messages with fn: tools=%d tool_choice=%v, want 1 tool and nil tool_choice (body: %s)", len(gotMsg.Tools), gotMsg.ToolChoice, bodyMsg)
	}
	droppedMsg := (&llm.Translation{Losses: lossesMsg}).DroppedPaths()
	if !slices.Contains(droppedMsg, "tools[1]") || !slices.Contains(droppedMsg, "tool_choice") {
		t.Errorf("messages with fn: dropped = %v, want tools[1] and tool_choice", droppedMsg)
	}
}
