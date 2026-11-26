package geminigenerate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
)

// Gemini's allowedFunctionNames is read into the model with the mode it came with: ANY as
// required, VALIDATED, in which the model picks from the allowed functions, as auto. It reaches
// Chat as its allowed_tools list, and written back as Gemini it names the same functions under
// the same mode.
func TestAllowedFunctionNamesToChat(t *testing.T) {
	for _, c := range []struct {
		name      string
		cfg       string
		wantMode  string
		wantNames []string
	}{
		{"ANY with two names", `{"mode":"ANY","allowedFunctionNames":["get_weather","get_time"]}`,
			"required", []string{"get_weather", "get_time"}},
		{"VALIDATED with one name", `{"mode":"VALIDATED","allowedFunctionNames":["get_weather"]}`,
			"auto", []string{"get_weather"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := `{"contents":[{"role":"user","parts":[{"text":"Weather in Oslo?"}]}],
			 "tools":[{"functionDeclarations":[
			   {"name":"get_weather","parameters":{"type":"object"}},
			   {"name":"get_time","parameters":{"type":"object"}}]}],
			 "toolConfig":{"functionCallingConfig":` + c.cfg + `}}`
			req := mustParse(t, in)

			body, losses, err := openaichat.EncodeRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range losses {
				if l.Path == "tool_choice" {
					t.Errorf("tool_choice reported as %s, want it carried", l.Kind)
				}
			}
			var out struct {
				ToolChoice struct {
					Type    string `json:"type"`
					Allowed struct {
						Mode  string `json:"mode"`
						Tools []struct {
							Type     string `json:"type"`
							Function struct {
								Name string `json:"name"`
							} `json:"function"`
						} `json:"tools"`
					} `json:"allowed_tools"`
				} `json:"tool_choice"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			tc := out.ToolChoice
			var names []string
			for _, tool := range tc.Allowed.Tools {
				if tool.Type != "function" {
					t.Errorf("allowed tool %q, want a function", tool.Type)
				}
				names = append(names, tool.Function.Name)
			}
			if tc.Type != "allowed_tools" || tc.Allowed.Mode != c.wantMode || !reflect.DeepEqual(names, c.wantNames) {
				t.Errorf("Chat tool_choice = %s, want allowed_tools mode %s tools %q", body, c.wantMode, c.wantNames)
			}

			// Written back as Gemini, the choice keeps its wire form.
			back, _, err := EncodeRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			var g struct {
				ToolConfig struct {
					FunctionCallingConfig struct {
						Mode                 string   `json:"mode"`
						AllowedFunctionNames []string `json:"allowedFunctionNames"`
					} `json:"functionCallingConfig"`
				} `json:"toolConfig"`
			}
			if err := json.Unmarshal(back, &g); err != nil {
				t.Fatal(err)
			}
			fc := g.ToolConfig.FunctionCallingConfig
			if fc.Mode+"/"+strings.Join(fc.AllowedFunctionNames, " ") != modeOf(c.cfg)+"/"+strings.Join(c.wantNames, " ") {
				t.Errorf("Gemini toolConfig = %s, want the mode and names it came with", back)
			}
		})
	}
}

// modeOf returns the functionCallingConfig mode in cfg, for a test that compares a round trip.
func modeOf(cfg string) string {
	var c struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(cfg), &c)
	return c.Mode
}

// A Chat request whose tool_choice is an allowed_tools list reaches Gemini as its function
// calling mode with allowedFunctionNames: required as ANY, auto as VALIDATED, the mode in which
// the model picks from the allowed functions.
func TestAllowedToolsFromChatToGemini(t *testing.T) {
	for _, c := range []struct{ mode, wantMode string }{
		{"required", "ANY"},
		{"auto", "VALIDATED"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			in := `{"model":"m","stream":true,
			 "tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}},
			          {"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}],
			 "tool_choice":{"type":"allowed_tools","allowed_tools":{"mode":"` + c.mode + `","tools":[
			   {"type":"function","function":{"name":"get_weather"}},
			   {"type":"function","function":{"name":"get_time"}}]}},
			 "messages":[{"role":"user","content":"Weather and time in Oslo?"}]}`
			req, err := openaichat.ParseRequest([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			body, losses, err := EncodeRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range losses {
				if l.Path == "tool_choice" {
					t.Errorf("tool_choice reported as %s, want it carried", l.Kind)
				}
			}
			var out struct {
				ToolConfig struct {
					FunctionCallingConfig struct {
						Mode                 string   `json:"mode"`
						AllowedFunctionNames []string `json:"allowedFunctionNames"`
					} `json:"functionCallingConfig"`
				} `json:"toolConfig"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			fc := out.ToolConfig.FunctionCallingConfig
			if fc.Mode != c.wantMode || strings.Join(fc.AllowedFunctionNames, " ") != "get_weather get_time" {
				t.Errorf("Gemini toolConfig = %s, want mode %s naming both allowed tools", body, c.wantMode)
			}
		})
	}
}
