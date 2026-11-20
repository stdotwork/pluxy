package anthropicmessages

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
)

// Messages has no allowed tool list. A choice that names one is carried by defining only the
// allowed tools, which is what the list asks for, under Messages' own mode: required as any, auto
// as auto. A tool the client defined but did not allow is off this request, so nothing is reported
// as lost.
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

// A hosted tool goes too: an allowed list names the function tools the model may call, and
// Messages' definition of the tools is its only narrowing, so a hosted tool the list does not
// name is off the request, without a loss of its own.
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

// A Responses request's allowed_tools list reaches Messages the same way: the allowed tools are
// all Messages defines, and the mode it came with becomes Messages' own.
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
