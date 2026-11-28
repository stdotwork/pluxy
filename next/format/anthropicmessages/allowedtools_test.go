package anthropicmessages

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestAllowedToolsNarrowMessagesTools(t *testing.T) {
	for _, c := range []struct{ mode, wantType string }{
		{"required", "any"},
		{"auto", "auto"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			in := `{"model":"m","stream":true,
			 "tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}},
			          {"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}},
			          {"type":"function","function":{"name":"read_file","parameters":{"type":"object"}}}],
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
				if l.Path == "tool_choice" || l.Path == "tools[2]" {
					t.Errorf("%s reported as %s, want the narrowing carried without a loss", l.Path, l.Kind)
				}
			}
			var out struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
				ToolChoice struct {
					Type string `json:"type"`
				} `json:"tool_choice"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range out.Tools {
				names = append(names, tool.Name)
			}
			if !reflect.DeepEqual(names, []string{"get_weather", "get_time"}) {
				t.Errorf("Messages tools %q, want the allowed ones only: %s", names, body)
			}
			if out.ToolChoice.Type != c.wantType {
				t.Errorf("Messages tool_choice %q, want %q: %s", out.ToolChoice.Type, c.wantType, body)
			}
		})
	}
}

func TestAllowedToolsDropHostedTools(t *testing.T) {
	in := `{"model":"m","stream":true,
	 "tools":[{"type":"function","name":"weather","parameters":{"type":"object"}},
	          {"type":"web_search"}],
	 "tool_choice":{"type":"allowed_tools","mode":"required","tools":[
	   {"type":"function","name":"weather"}]},
	 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather in Oslo?"}]}]}`
	req, err := openairesponses.ParseRequest([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	body, losses, err := EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range losses {
		if l.Path == "tools[1]" {
			t.Errorf("tools[1] reported as %s, want the hosted tool off the request without a loss", l.Kind)
		}
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range out.Tools {
		names = append(names, tool.Name)
	}
	if !reflect.DeepEqual(names, []string{"weather"}) {
		t.Errorf("Messages tools %q, want the allowed function only: %s", names, body)
	}
	if strings.Contains(string(body), "web_search") {
		t.Errorf("Messages body still has the hosted tool: %s", body)
	}
}

func TestAllowedToolsMatchNoTool(t *testing.T) {
	request := func(t *testing.T, allowed string) llm.Request {
		t.Helper()
		in := `{"model":"m","stream":true,
		 "tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}],
		 "tool_choice":{"type":"allowed_tools","mode":"required","tools":[` + allowed + `]},
		 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather?"}]}]}`
		req, err := openairesponses.ParseRequest([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	body, _, err := EncodeRequest(request(t, `{"type":"function","name":"ghost"},{"type":"function","name":"goblin"}`))
	var e *llm.Error
	if !asError(err, &e) {
		t.Fatalf("EncodeRequest = %s, %v, want an invalid request error", body, err)
	}
	if e.Code != llm.CodeInvalidRequest || e.Scope != llm.ScopeRequest || e.Status != http.StatusBadRequest {
		t.Errorf("error %+v, want an invalid request error for this request", e)
	}
	if !strings.Contains(e.Message, "ghost, goblin") {
		t.Errorf("error message %q, want it to name the allowed names this request does not define", e.Message)
	}

	body, losses, err := EncodeRequest(request(t, `{"type":"function","name":"weather"},{"type":"function","name":"ghost"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reported []llm.Loss
	for _, l := range losses {
		if l.Path == "tool_choice" {
			reported = append(reported, l)
		}
	}
	if len(reported) != 1 || reported[0].Kind != llm.LossDropped || !strings.Contains(reported[0].Detail, "ghost") {
		t.Errorf("losses %+v, want ghost reported as dropped at tool_choice: %s", reported, body)
	}
	var out struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "weather" {
		t.Errorf("Messages tools %+v, want the allowed tool it defines: %s", out.Tools, body)
	}
}

func TestAllowedToolsFromResponsesToMessages(t *testing.T) {
	in := `{"model":"m","stream":true,
	 "tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"}},
	          {"type":"function","name":"get_time","parameters":{"type":"object"}}],
	 "tool_choice":{"type":"allowed_tools","mode":"required","tools":[
	   {"type":"function","name":"get_weather"}]},
	 "input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Weather?"}]}]}`
	req, err := openairesponses.ParseRequest([]byte(in))
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
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice struct {
			Type string `json:"type"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "get_weather" {
		t.Errorf("Messages tools %+v, want only get_weather: %s", out.Tools, body)
	}
	if out.ToolChoice.Type != "any" {
		t.Errorf("Messages tool_choice %q, want any: %s", out.ToolChoice.Type, body)
	}
}
