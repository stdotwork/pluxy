package geminigenerate_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
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

// Google documents structured output beside a built-in tool (including Grounding with Google
// Search) only on Gemini 3 and 4 (https://ai.google.dev/gemini-api/docs/structured-output#tools).
func TestWebSearchWithStructuredOutput(t *testing.T) {
	for model, want := range map[string]bool{
		"gemini-2.5-flash":                   false,
		"gemini-2.5-pro":                     false,
		"gemini-2.0-flash":                   false,
		"models/gemini-2.5-pro":              false,
		"gemini-robotics-er-2-preview":       false,
		"gemini-nano-banana-2.1":             false,
		"gemini-live-2.5-flash-native-audio": false,
		"gemini-3-flash":                     true,
		"gemini-3.8-flash":                   true,
		"gemini-4-neon":                      true,
		"models/gemini-4-hydrogen":           true,
	} {
		t.Run(model+"/json_schema", func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(`{"model":"` + model + `","input":"latest news",
				"text":{"format":{"type":"json_schema","name":"r","schema":{"type":"object"}}},
				"tools":[{"type":"web_search"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{WebSearch: true})
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools            []map[string]json.RawMessage `json:"tools"`
				GenerationConfig struct {
					ResponseMimeType string `json:"responseMimeType"`
				} `json:"generationConfig"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.GenerationConfig.ResponseMimeType != "application/json" {
				t.Errorf("responseMimeType = %q, want application/json (body: %s)", got.GenerationConfig.ResponseMimeType, body)
			}
			if sent := len(got.Tools) == 1 && got.Tools[0]["googleSearch"] != nil; sent != want {
				t.Errorf("googleSearch sent = %v, want %v (body: %s)", sent, want, body)
			}
			if lost := slices.Contains((&llm.Translation{Losses: losses}).DroppedPaths(), "tools[0]"); lost == want {
				t.Errorf("tools[0] dropped = %v with sent = %v: %+v", lost, want, losses)
			}
		})
	}

	t.Run("chat_json_object_gemini2_drops_search", func(t *testing.T) {
		req, err := openaichat.ParseRequest([]byte(`{
			"model":"gemini-2.5-flash",
			"messages":[{"role":"user","content":"latest news"}],
			"response_format":{"type":"json_object"},
			"web_search_options":{}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools            []map[string]json.RawMessage `json:"tools"`
			GenerationConfig struct {
				ResponseMimeType string `json:"responseMimeType"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 0 {
			t.Errorf("tools = %v, want omitted (body: %s)", got.Tools, body)
		}
		if got.GenerationConfig.ResponseMimeType != "application/json" {
			t.Errorf("responseMimeType = %q, want application/json", got.GenerationConfig.ResponseMimeType)
		}
		if !slices.Contains((&llm.Translation{Losses: losses}).DroppedPaths(), "web_search_options") {
			t.Errorf("dropped = %v, want web_search_options in losses %+v", (&llm.Translation{Losses: losses}).DroppedPaths(), losses)
		}
	})

	t.Run("required_tool_choice_reported_when_search_dropped", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{
			"model":"gemini-2.5-flash",
			"input":"latest news",
			"text":{"format":{"type":"json_schema","name":"r","schema":{"type":"object"}}},
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
		if len(got.Tools) != 0 || len(got.ToolConfig) != 0 {
			t.Errorf("want neither tools nor toolConfig, got body: %s", body)
		}
		dropped := (&llm.Translation{Losses: losses}).DroppedPaths()
		if !slices.Contains(dropped, "tools[0]") || !slices.Contains(dropped, "tool_choice") {
			t.Errorf("dropped = %v, want both tools[0] and tool_choice in losses %+v", dropped, losses)
		}
	})

	t.Run("own_gemini_request_preserved", func(t *testing.T) {
		raw := []byte(`{"contents":[{"role":"user","parts":[{"text":"latest news"}]}],"generationConfig":{"responseMimeType":"application/json"},"tools":[{"googleSearch":{}}]}`)
		req, err := geminigenerate.ParseRequest("gemini-2.5-flash", false, raw)
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(losses) != 0 {
			t.Errorf("own Gemini request: unexpected losses %+v", losses)
		}
		var got struct {
			Tools            []map[string]json.RawMessage `json:"tools"`
			GenerationConfig struct {
				ResponseMimeType string `json:"responseMimeType"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 1 || got.Tools[0]["googleSearch"] == nil {
			t.Errorf("own Gemini request: tools = %v, want googleSearch preserved (body: %s)", got.Tools, body)
		}
		if got.GenerationConfig.ResponseMimeType != "application/json" {
			t.Errorf("own Gemini request: responseMimeType = %q, want application/json", got.GenerationConfig.ResponseMimeType)
		}
		// Encoding with WebSearch: false produces the exact same bytes for a Gemini client's own body.
		bodyNoOpt, lossesNoOpt, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, bodyNoOpt) || len(lossesNoOpt) != 0 {
			t.Errorf("own Gemini request differed with WebSearch=false:\nwith:    %s\nwithout: %s\nlosses:  %+v", body, bodyNoOpt, lossesNoOpt)
		}
	})

	t.Run("gemini4_structured_output_byte_identical", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{"model":"gemini-4-neon","input":"latest news",
			"text":{"format":{"type":"json_schema","name":"r","schema":{"type":"object"}}},
			"tools":[{"type":"web_search"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := geminigenerate.EncodeRequestWith(req, geminigenerate.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		want := `{"contents":[{"parts":[{"text":"latest news"}],"role":"user"}],"generationConfig":{"responseJsonSchema":{"type":"object"},"responseMimeType":"application/json"},"model":"gemini-4-neon","safetySettings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"OFF"},{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"OFF"},{"category":"HARM_CATEGORY_SEXUALLY_EXPLICIT","threshold":"OFF"},{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","threshold":"OFF"},{"category":"HARM_CATEGORY_CIVIC_INTEGRITY","threshold":"BLOCK_NONE"}],"tools":[{"googleSearch":{}}]}`
		if string(body) != want {
			t.Errorf("gemini-4-neon body changed:\ngot:  %s\nwant: %s", body, want)
		}
		if len(losses) != 0 {
			t.Errorf("gemini-4-neon unexpected losses: %+v", losses)
		}
	})
}
