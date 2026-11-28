package openairesponses_test

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

func TestCodeExecutionNotSentToModelsWithoutResponses(t *testing.T) {
	for _, model := range []string{"o1-mini", "gpt-4o-mini-audio-preview"} {
		req, err := anthropicmessages.ParseRequest([]byte(`{"model":"` + model + `","max_tokens":9,
			"messages":[{"role":"user","content":"compute"}],
			"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		body, _, err := openairesponses.EncodeRequestWith(req, openairesponses.EncodeOptions{CodeExecution: true})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 0 {
			t.Errorf("%s: tools = %s, want none", model, got.Tools)
		}
	}
}

func TestTranslatedCodeToolAsksForOutputs(t *testing.T) {
	const want = "code_interpreter_call.outputs"
	include := func(t *testing.T, body []byte) (list []string, present bool) {
		t.Helper()
		var got struct {
			Include *[]string `json:"include"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.Include == nil {
			return nil, false
		}
		return *got.Include, true
	}
	encode := func(t *testing.T, req llm.Request, opts openairesponses.EncodeOptions) []byte {
		t.Helper()
		body, _, err := openairesponses.EncodeRequestWith(req, opts)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	messages := func(t *testing.T, model string) llm.Request {
		t.Helper()
		req, err := anthropicmessages.ParseRequest([]byte(`{"model":"` + model + `","max_tokens":9,
			"messages":[{"role":"user","content":"compute"}],
			"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	gemini := func(t *testing.T) llm.Request {
		t.Helper()
		req, err := geminigenerate.ParseRequest("gpt-5.2", false, []byte(`{
			"contents":[{"role":"user","parts":[{"text":"compute"}]}],"tools":[{"codeExecution":{}}]}`))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}

	t.Run("sent for a translated tool", func(t *testing.T) {
		for name, req := range map[string]llm.Request{"messages": messages(t, "gpt-5.2"), "gemini": gemini(t)} {
			got, ok := include(t, encode(t, req, openairesponses.EncodeOptions{CodeExecution: true}))
			if !ok || !slices.Equal(got, []string{want}) {
				t.Errorf("%s client: include = %v (present %v), want [%s]", name, got, ok, want)
			}
		}
	})

	t.Run("merged with the client's include, once", func(t *testing.T) {
		for _, in := range []string{`["web_search_call.action.sources"]`, `["web_search_call.action.sources","` + want + `"]`} {
			req, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-5.2","input":"compute","include":` + in + `}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Tools = append(req.Tools, llm.Tool{Type: llm.ToolCodeExecution})
			got, _ := include(t, encode(t, req, openairesponses.EncodeOptions{CodeExecution: true}))
			if !slices.Equal(got, []string{"web_search_call.action.sources", want}) {
				t.Errorf("client include %s: sent %v", in, got)
			}
		}
	})

	t.Run("absent where the tool is not sent", func(t *testing.T) {
		cases := map[string]struct {
			req  llm.Request
			opts openairesponses.EncodeOptions
		}{
			"host without the tool":   {messages(t, "gpt-5.2"), openairesponses.EncodeOptions{}},
			"model without the tool":  {messages(t, "gpt-5.4-pro"), openairesponses.EncodeOptions{CodeExecution: true}},
			"gemini, host without it": {gemini(t), openairesponses.EncodeOptions{}},
		}
		for name, c := range cases {
			if got, ok := include(t, encode(t, c.req, c.opts)); ok {
				t.Errorf("%s: include = %v, want none", name, got)
			}
		}
	})

	t.Run("a Responses client's own tool and include are untouched", func(t *testing.T) {
		for _, in := range []string{
			`{"model":"gpt-5.2","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`,
			`{"model":"gpt-5.2","input":"run","include":["web_search_call.action.sources"],"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`,
		} {
			req, err := openairesponses.ParseRequest([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			body := encode(t, req, openairesponses.EncodeOptions{CodeExecution: true})
			var sent, asked map[string]json.RawMessage
			_ = json.Unmarshal(body, &sent)
			_ = json.Unmarshal([]byte(in), &asked)
			if !bytes.Equal(sent["include"], asked["include"]) {
				t.Errorf("include = %s, client sent %s", sent["include"], asked["include"])
			}
		}
	})
}
