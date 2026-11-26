package nativeonly

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

type nativeCase struct {
	format string
	body   string
	parse  func(t *testing.T, body string) llm.Request
}

func nativeCases() []nativeCase {
	return []nativeCase{
		{llm.FormatOpenAIChat,
			`{"model":"gpt-x","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}}],"user":"u-1","metadata":{"k":"v"}}`,
			func(t *testing.T, b string) llm.Request {
				r, err := openaichat.ParseRequest([]byte(b))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}},
		{llm.FormatOpenAIResponses,
			`{"model":"gpt-x","input":[{"role":"user","content":"hi"}],"instructions":"be brief","tools":[{"type":"function","name":"f","description":"d","parameters":{"type":"object"}}],"metadata":{"k":"v"}}`,
			func(t *testing.T, b string) llm.Request {
				r, err := openairesponses.ParseRequest([]byte(b))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}},
		{llm.FormatAnthropicMessages,
			`{"model":"claude-x","max_tokens":100,"system":"be brief","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"f","description":"d","input_schema":{"type":"object"}}],"metadata":{"user_id":"u"}}`,
			func(t *testing.T, b string) llm.Request {
				r, err := anthropicmessages.ParseRequest([]byte(b))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}},
		{llm.FormatGeminiGenerate,
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"be brief"}]},"tools":[{"functionDeclarations":[{"name":"f","description":"d"}]}],"safetySettings":[]}`,
			func(t *testing.T, b string) llm.Request {
				r, err := geminigenerate.ParseRequest("gem-x", true, []byte(b))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}},
	}
}

func TestParseRestoresWhatTheFormatParses(t *testing.T) {
	for _, c := range nativeCases() {
		t.Run(c.format, func(t *testing.T) {
			want := c.parse(t, c.body)
			if len(want.Items) == 0 || len(want.Tools) == 0 {
				t.Fatalf("test body parses to %d items, %d tools; want both", len(want.Items), len(want.Tools))
			}
			effort := "high"
			req := llm.Request{
				Model:      "renamed-upstream",
				Stream:     true,
				Options:    llm.Options{ThinkingSuffix: "high", ReasoningEffort: effort},
				DropPolicy: llm.DropReject,
				Caller:     "caller-7",
				Native:     &llm.Native{Format: c.format, Body: json.RawMessage(c.body), Headers: map[string]string{"h": "v"}},
			}
			req.Native.Parsed = req.Native.Body
			sent := Strip(req)
			if err := Parse(&sent); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sent.Items, want.Items) || !reflect.DeepEqual(sent.Tools, want.Tools) || !reflect.DeepEqual(sent.Origin, want.Origin) {
				t.Errorf("restored items/tools/origin differ from the parser's:\n got %+v\nwant %+v", sent, want)
			}
			if want.Origin == nil {
				t.Errorf("test body has no origin; it does not check Origin")
			}
			full := req
			full.Items, full.Tools, full.Origin = want.Items, want.Tools, want.Origin
			if !reflect.DeepEqual(sent, full) {
				t.Errorf("request after Strip+Parse differs from the full request:\n got %+v\nwant %+v", sent, full)
			}
		})
	}
}

func TestParseLeavesTheRestAlone(t *testing.T) {
	body := nativeCases()[0].body
	req := llm.Request{
		Model:      "renamed",
		Options:    llm.Options{ThinkingSuffix: "low"},
		DropPolicy: llm.DropReject,
		Caller:     "c",
		Native:     &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(body), Headers: map[string]string{"a": "b"}},
	}
	native := *req.Native
	if err := Parse(&req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "renamed" || req.Options.ThinkingSuffix != "low" || req.DropPolicy != llm.DropReject || req.Caller != "c" {
		t.Errorf("Parse changed fields it must keep: %+v", req)
	}
	if !reflect.DeepEqual(*req.Native, native) {
		t.Errorf("Native changed: %+v", req.Native)
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    *llm.Native
	}{
		{"no native", nil},
		{"unknown format", &llm.Native{Format: "openai.embeddings", Body: json.RawMessage(`{}`)}},
		{"unparsable body", &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{"messages":`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := llm.Request{Model: "m", Native: tc.n}
			if err := Parse(&req); err == nil {
				t.Errorf("Parse = nil, want an error")
			}
		})
	}
}

func TestCan(t *testing.T) {
	body := json.RawMessage(nativeCases()[0].body)
	parsed := func(n llm.Native) llm.Native { n.Parsed = n.Body; return n }
	good := parsed(llm.Native{Format: llm.FormatOpenAIChat, Body: body})
	unset := llm.Native{Format: llm.FormatOpenAIChat, Body: body}
	differs := good
	differs.Parsed = json.RawMessage(`{"model":"other"}`)
	unknown := parsed(llm.Native{Format: "openai.embeddings", Body: body})
	badUTF8 := parsed(llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage("{\"a\":\"\xff\"}")})
	for _, tc := range []struct {
		name string
		n    *llm.Native
		want bool
	}{
		{"parsed and valid", &good, true},
		{"nil native", nil, false},
		{"unknown format", &unknown, false},
		{"parsed unset", &unset, false},
		{"parsed differs from body", &differs, false},
		{"body not UTF-8", &badUTF8, false},
	} {
		if got := Can(llm.Request{Native: tc.n}); got != tc.want {
			t.Errorf("%s: Can = %v, want %v", tc.name, got, tc.want)
		}
	}
	for _, c := range nativeCases() {
		n := parsed(llm.Native{Format: c.format, Body: json.RawMessage(c.body)})
		if !Can(llm.Request{Native: &n}) {
			t.Errorf("%s: Can = false, want true", c.format)
		}
	}
}

func TestStripClearsExactlyItemsToolsOrigin(t *testing.T) {
	req := llm.Request{
		Model: "m", Stream: true, Items: []llm.Item{{}}, Tools: []llm.Tool{{Name: "t"}},
		Options: llm.Options{ThinkingSuffix: "x"}, Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)},
		Origin: &llm.Origin{Format: llm.FormatOpenAIChat}, DropPolicy: llm.DropReject, Caller: "c",
	}
	got := Strip(req)
	want := req
	want.Items, want.Tools, want.Origin = nil, nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Strip = %+v, want %+v", got, want)
	}
	if len(req.Items) != 1 || req.Origin == nil {
		t.Errorf("Strip changed its argument")
	}
}
