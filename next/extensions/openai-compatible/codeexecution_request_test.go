package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestCodeExecutionOpenAICompatibleTargets(t *testing.T) {
	// 1. Messages -> OpenAI Responses at api.openai.com (supported model) gets code_interpreter.
	t.Run("messages_to_responses_official", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"gpt-5.4",
			"max_tokens":1024,
			"messages":[{"role":"user","content":"compute"}],
			"tools":[{"type":"code_execution_20250825","name":"code_execution"}],
			"tool_choice":{"type":"tool","name":"code_execution"}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBody(req, llm.FormatOpenAIResponses, settings{BaseURL: "https://api.openai.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools      []map[string]any `json:"tools"`
			ToolChoice map[string]any   `json:"tool_choice"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["type"] != "code_interpreter" {
			t.Fatalf("tools = %v, want code_interpreter (body: %s)", got.Tools, body)
		}
		if got.ToolChoice["type"] != "code_interpreter" {
			t.Errorf("tool_choice = %v, want {type:code_interpreter}", got.ToolChoice)
		}
		if tr.Dropped() {
			t.Errorf("unexpected dropped losses: %+v", tr.Losses)
		}

		// Third-party host drops tool and tool_choice.
		body3p, tr3p, err := buildBody(req, llm.FormatOpenAIResponses, settings{BaseURL: "https://openrouter.ai/api/v1"})
		if err != nil {
			t.Fatal(err)
		}
		var got3p struct {
			Tools      []map[string]any `json:"tools"`
			ToolChoice any              `json:"tool_choice"`
		}
		_ = json.Unmarshal(body3p, &got3p)
		if len(got3p.Tools) != 0 || got3p.ToolChoice != nil {
			t.Errorf("third-party: tools=%v tool_choice=%v, want both empty", got3p.Tools, got3p.ToolChoice)
		}
		if !slices.Contains(tr3p.DroppedPaths(), "tools[0]") || !slices.Contains(tr3p.DroppedPaths(), "tool_choice") {
			t.Errorf("third-party: dropped = %v, want tools[0] and tool_choice", tr3p.DroppedPaths())
		}
	})

	// 2. Chat Completions has no code execution tool even at api.openai.com.
	t.Run("responses_to_chat_unsupported", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{
			"model":"gpt-5.4",
			"input":"compute",
			"tools":[{"type":"code_interpreter","container":{"type":"auto"}}],
			"tool_choice":{"type":"code_interpreter"}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBody(req, llm.FormatOpenAIChat, settings{BaseURL: "https://api.openai.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools      []map[string]any `json:"tools"`
			ToolChoice any              `json:"tool_choice"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 0 || got.ToolChoice != nil {
			t.Errorf("chat: tools=%v tool_choice=%v, want both empty (body: %s)", got.Tools, got.ToolChoice, body)
		}
		if !slices.Contains(tr.DroppedPaths(), "tools[0]") || !slices.Contains(tr.DroppedPaths(), "tool_choice") {
			t.Errorf("chat: dropped = %v, want tools[0] and tool_choice", tr.DroppedPaths())
		}
	})

	// 3. Responses -> Anthropic Messages at api.anthropic.com gets code_execution_20250825; third-party drops.
	t.Run("responses_to_messages", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{
			"model":"claude-sonnet-4-6",
			"input":"compute",
			"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBody(req, llm.FormatAnthropicMessages, settings{BaseURL: "https://api.anthropic.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["type"] != "code_execution_20250825" {
			t.Fatalf("anthropic official: tools = %v, want code_execution_20250825", got.Tools)
		}
		if tr.Dropped() {
			t.Errorf("anthropic official: unexpected dropped losses: %+v", tr.Losses)
		}

		body3p, tr3p, err := buildBody(req, llm.FormatAnthropicMessages, settings{BaseURL: "https://proxy.example.com/v1"})
		if err != nil {
			t.Fatal(err)
		}
		var got3p struct {
			Tools []map[string]any `json:"tools"`
		}
		_ = json.Unmarshal(body3p, &got3p)
		if len(got3p.Tools) != 0 || !tr3p.Dropped() {
			t.Errorf("anthropic third-party: tools=%v dropped=%v", got3p.Tools, tr3p.Dropped())
		}
	})

	// 4. Responses -> Gemini GenerateContent at generativelanguage.googleapis.com gets codeExecution; third-party drops.
	t.Run("responses_to_gemini", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{
			"model":"gemini-4-hydrogen",
			"input":"compute",
			"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		req = roundTripReq(t, req)
		body, tr, err := buildBody(req, llm.FormatGeminiGenerate, settings{BaseURL: "https://generativelanguage.googleapis.com/v1beta"})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body, &got)
		if len(got.Tools) != 1 || got.Tools[0]["codeExecution"] == nil {
			t.Fatalf("gemini official: tools = %v, want codeExecution", got.Tools)
		}
		if tr.Dropped() {
			t.Errorf("gemini official: unexpected dropped losses: %+v", tr.Losses)
		}

		body3p, tr3p, err := buildBody(req, llm.FormatGeminiGenerate, settings{BaseURL: "https://proxy.example.com/v1beta"})
		if err != nil {
			t.Fatal(err)
		}
		var got3p struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(body3p, &got3p)
		if len(got3p.Tools) != 0 || !tr3p.Dropped() {
			t.Errorf("gemini third-party: tools=%v dropped=%v", got3p.Tools, tr3p.Dropped())
		}
	})
}

func TestCodeExecutionOpenAICompatibleSameFormatByteIdentical(t *testing.T) {
	rawResp := []byte(`{"model":"gpt-5.4","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}}],"tool_choice":{"type":"code_interpreter"}}`)
	reqResp, err := openairesponses.ParseRequest(rawResp)
	if err != nil {
		t.Fatal(err)
	}
	reqResp = roundTripReq(t, reqResp)
	body, tr, err := buildBody(reqResp, llm.FormatOpenAIResponses, settings{BaseURL: "https://proxy.example.com/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Passthrough || !bytes.Equal(body, rawResp) {
		t.Errorf("responses passthrough changed: passthrough=%v body=%s", tr.Passthrough, body)
	}

	rawGem := []byte(`{"contents":[{"role":"user","parts":[{"text":"run"}]}],"tools":[{"codeExecution":{}}]}`)
	reqGem, err := geminigenerate.ParseRequest("gemini-4-hydrogen", false, rawGem)
	if err != nil {
		t.Fatal(err)
	}
	reqGem = roundTripReq(t, reqGem)
	bodyGem, trGem, err := buildBody(reqGem, llm.FormatGeminiGenerate, settings{BaseURL: "https://proxy.example.com/v1beta"})
	if err != nil {
		t.Fatal(err)
	}
	if !trGem.Passthrough || !bytes.Equal(bodyGem, rawGem) {
		t.Errorf("gemini passthrough changed: passthrough=%v body=%s", trGem.Passthrough, bodyGem)
	}
}
