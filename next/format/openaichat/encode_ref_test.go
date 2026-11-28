package openaichat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

type outRequestRef struct {
	Model         string            `json:"model"`
	Messages      []json.RawMessage `json:"messages"`
	Stream        bool              `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	MaxTokens         *int              `json:"max_tokens,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	Stop              []string          `json:"stop,omitempty"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    any               `json:"response_format,omitempty"`
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
}

func encodeRequestRef(req llm.Request) ([]byte, []llm.Loss, error) {
	e := &encoder{}
	names := ToolNames(req)
	out := outRequestRef{
		Model:           req.Model,
		Messages:        []json.RawMessage{},
		Stream:          req.Stream,
		MaxTokens:       req.Options.MaxOutputTokens,
		Temperature:     req.Options.Temperature,
		TopP:            req.Options.TopP,
		Stop:            req.Options.Stop,
		ReasoningEffort: req.Options.ReasoningEffort,
	}
	if req.Stream {
		out.StreamOptions = &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true}
	}
	if rf := req.Options.ResponseFormat; rf != nil {
		out.ResponseFormat = encodeResponseFormat(rf)
	}
	for i, t := range req.Tools {
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				out.Tools = append(out.Tools, t.Origin.Raw)
			} else {
				e.lose(llm.LossDropped, path(t.Origin, fmt.Sprintf("tools[%d]", i)), "Chat Completions has no such tool")
			}
			continue
		}
		ct := chatTool{Type: "function", Function: chatFunction{Name: ToolName(names, t.Name), Description: t.Description, Parameters: t.Parameters, Strict: t.Strict}}
		out.Tools = append(out.Tools, marshal(ct, e.kept(t.Origin)))
	}
	if len(out.Tools) > 0 {
		out.ParallelToolCalls = req.Options.ParallelToolCalls
		if tc := req.Options.ToolChoice; tc != nil {
			out.ToolChoice = encodeToolChoice(tc, names)
		}
	}

	var msgs []*outMessage
	reasoning := ""
	flushReasoning := func() {
		if reasoning != "" {
			empty := ""
			msgs = append(msgs, &outMessage{Role: llm.RoleAssistant, Content: &empty, ReasoningContent: reasoning})
			reasoning = ""
		}
	}
	for i, it := range req.Items {
		p := path(it.Origin, fmt.Sprintf("items[%d]", i))
		switch {
		case it.Type == llm.ItemTypeToolCall, it.Type == llm.ItemTypeReasoning, it.Type == llm.ItemTypeNative:
		case it.Type == llm.ItemTypeMessage && it.Role == llm.RoleAssistant:
		default:
			flushReasoning()
		}
		switch it.Type {
		case llm.ItemTypeReasoning:
			if it.Origin != nil && it.Origin.Format != format && it.Text != "" {
				e.lose(llm.LossApproximated, p, "reasoning summary sent as the next assistant message's reasoning_content")
			}
			e.kept(it.Origin)
			reasoning = joinReasoning(reasoning, it.Text)
		case llm.ItemTypeMessage:
			if emptyForeign(it, format) {
				e.kept(it.Origin)
				break
			}
			content, sendable, dropped, err := e.content(it, p)
			if err != nil {
				return nil, nil, err
			}
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to Chat Completions", p)
			}
			raw := e.kept(it.Origin)
			if n := len(msgs); it.Role == llm.RoleAssistant && n > 0 && reasoning == "" && len(raw) == 0 && content != nil {
				if last := msgs[n-1]; last.Role == llm.RoleAssistant && len(last.ToolCalls) > 0 && last.ToolCallID == "" {
					last.Content = joinContent(last.Content, content)
					e.lose(llm.LossApproximated, p, "assistant text after tool calls sent in the message with the calls")
					break
				}
			}
			m := &outMessage{Role: it.Role, Content: content, raw: raw}
			if it.Role == llm.RoleAssistant {
				m.ReasoningContent, reasoning = reasoning, ""
			}
			msgs = append(msgs, m)
		case llm.ItemTypeToolCall:
			var last *outMessage
			if n := len(msgs); n > 0 && msgs[n-1].Role == llm.RoleAssistant && msgs[n-1].ToolCallID == "" && reasoning == "" {
				last = msgs[n-1]
			} else {
				last = &outMessage{Role: llm.RoleAssistant}
				msgs = append(msgs, last)
			}
			if reasoning != "" {
				last.ReasoningContent, reasoning = reasoning, ""
			}
			if it.Custom {
				var tc customToolCall
				tc.ID, tc.Type = it.CallID, "custom"
				tc.Custom.Name, tc.Custom.Input = it.Name, it.Arguments
				last.ToolCalls = append(last.ToolCalls, marshal(tc, e.kept(it.Origin)))
				break
			}
			var tc chatToolCall
			tc.ID, tc.Type = it.CallID, "function"
			tc.Function.Name, tc.Function.Arguments = ToolName(names, it.Name), toolArguments(it.Arguments, it.Origin, p, e)
			origin, extra := geminiSignature(it.Origin)
			raw := e.kept(origin)
			if extra != nil {
				raw = rawjson.Merge(extra, raw)
			}
			last.ToolCalls = append(last.ToolCalls, marshal(tc, raw))
		case llm.ItemTypeToolResult:
			msgs = append(msgs, &outMessage{Role: "tool", Content: it.Output, ToolCallID: it.CallID, raw: e.kept(it.Origin)})
		case llm.ItemTypeNative:
			e.lose(llm.LossDropped, p, "Chat Completions has no such item")
		default:
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("%s: unsupported item type %q", p, it.Type), Scope: llm.ScopeRequest}
		}
	}
	flushReasoning()
	for _, m := range msgs {
		if m.Role == llm.RoleAssistant && m.Content == nil && len(m.ToolCalls) == 0 {
			m.Content = ""
		}
		out.Messages = append(out.Messages, marshal(m, m.raw))
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return rawjson.Merge(b, e.kept(req.Origin)), e.losses, nil
}

func TestEncodeRequestMatchesReference(t *testing.T) {
	reqs := refBuiltRequests(t)
	reqs = append(reqs, refGeneratedRequests()...)
	n := 0
	for _, c := range reqs {
		t.Run(c.name, func(t *testing.T) {
			got, gotLosses, gotErr := EncodeRequest(c.req)
			want, wantLosses, wantErr := encodeRequestRef(c.req)
			if (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("error = %v, reference %v", gotErr, wantErr)
			}
			if gotErr != nil {
				if gotErr.Error() != wantErr.Error() {
					t.Fatalf("error = %v, reference %v", gotErr, wantErr)
				}
				return
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("body =\n %s\nreference\n %s", got, want)
			}
			if !reflect.DeepEqual(gotLosses, wantLosses) {
				t.Fatalf("losses = %+v, reference %+v", gotLosses, wantLosses)
			}
		})
		n++
	}
	t.Logf("%d requests compared with the reference", n)
}

type refCase struct {
	name string
	req  llm.Request
}

func refBuiltRequests(t *testing.T) []refCase {
	t.Helper()
	bodies := []struct{ name, body string }{
		{"parse-then-encode", `{"model":"m","stream":true,"max_tokens":50,"temperature":0.5,"stop":"END",
	"tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object"}}}],
	"messages":[
	 {"role":"system","content":"Be brief."},
	 {"role":"user","content":[{"type":"text","text":"Weather in Oslo and Bergen?"},{"type":"image_url","image_url":{"url":"https://example.com/a.png","detail":"low"}},{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]},
	 {"role":"assistant","content":"Checking.","tool_calls":[
	   {"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}},
	   {"id":"call_2","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Bergen\"}"}}]},
	 {"role":"tool","tool_call_id":"call_1","content":"4C rain"},
	 {"role":"tool","tool_call_id":"call_2","content":[{"type":"text","text":"6C sun"}]}
	]}`},
		{"keeps-unmodelled-fields", `{"model":"m","stream":false,"metadata":{"team":"a"},"x_vendor":{"k":1},
	"tools":[{"type":"function","x_tool":true,"function":{"name":"f","parameters":{"type":"object"},"strict":true,"x_fn":2}},
	 {"type":"custom","custom":{"name":"patch"}}],
	"messages":[
	 {"role":"user","name":"alice","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}],"x_msg":"u"},
	 {"role":"assistant","content":"Calling.","reasoning_content":"Need f.","tool_calls":[
	   {"id":"call_1","type":"function","x_call":3,"function":{"name":"f","arguments":"{}"}}]},
	 {"role":"tool","tool_call_id":"call_1","content":"done","x_tool_msg":4}
	]}`},
		{"keeps-nested-fields", `{"model":"m","stream":true,"stream_options":{"include_usage":true,"include_obfuscation":false},
	"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":{"type":"object"},"x_schema":1}},
	"messages":[
	 {"role":"user","content":[{"type":"text","text":""},{"type":"text","text":"hi"}]},
	 {"role":"assistant","name":"bot","tool_calls":[{"id":"c1","type":"custom","custom":{"name":"patch","input":"*** Begin"}}]},
	 {"role":"tool","tool_call_id":"c1","content":"ok"}
	]}`},
		{"kept-fields-with-space", `{ "model" : "m" , "stream" : true ,
	"tools" : [ { "type" : "function" , "function" : { "name" : "f" , "parameters" : { "type" : "object" , "properties" : { "a" : { "type" : "string" } } } } } ] ,
	"messages" : [ { "role" : "user" , "content" : "hi" , "x_kept" : { "a" : [ 1 , 2 ] } } ] }`},
	}
	var out []refCase
	for _, b := range bodies {
		req, err := ParseRequest([]byte(b.body))
		if err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		out = append(out, refCase{name: "parsed/" + b.name, req: req})
		req.Stream = !req.Stream
		out = append(out, refCase{name: "parsed-stream-toggled/" + b.name, req: req})
	}
	strict := true
	out = append(out, []refCase{
		{"empty-assistant-content", llm.Request{Model: "m", Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}, TextContent: true},
			{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: ""}}},
		}}},
		{"directly-stated", llm.Request{Model: "m", Stream: true,
			Items: []llm.Item{
				{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}},
				{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "f", Arguments: "{}"},
				{Type: llm.ItemTypeToolResult, CallID: "c1", Output: "ok"},
			},
			Tools:   []llm.Tool{{Name: "f", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
			Options: llm.Options{MaxOutputTokens: ptr(10), Temperature: ptrF(0.25), ReasoningEffort: "high"}}},
	}...)
	return out
}

func ptr[T any](v T) *T       { return &v }
func ptrF(v float64) *float64 { return &v }

func refGeneratedRequests() []refCase {
	strict := true
	notStrict := false
	texts := []struct{ name, text string }{
		{"plain", "hello"},
		{"empty", ""},
		{"escapes", "a\"b\\c\nd\te\rf\bg\fh"},
		{"unicode", "héllo ☃ 𝄞 日本語"},
		{"line-separators", "a\u2028b\u2029c"},
		{"html", "<script>&</script>"},
		{"invalid-utf8", "a\xffb\xfec\x80"},
		{"control", "a\x01b\x1fc\x7f"},
		{"long", string(bytes.Repeat([]byte("abc def "), 200))},
	}
	kept := []struct {
		name string
		o    *llm.Origin
	}{
		{"none", nil},
		{"chat-fields", &llm.Origin{Format: format, Path: "p", Raw: json.RawMessage(`{"x_vendor":{"k":1},"z":null}`)}},
		{"chat-fields-spaced", &llm.Origin{Format: format, Path: "p", Raw: json.RawMessage(`{ "a" : 1 , "b" : [ 1 , 2 ] }`)}},
		{"chat-empty", &llm.Origin{Format: format, Path: "p", Raw: json.RawMessage(`{}`)}},
		{"other-format", &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "p", Raw: json.RawMessage(`{"x":1}`)}},
		{"gemini-signature", &llm.Origin{Format: llm.FormatGeminiGenerate, Path: "p", Raw: json.RawMessage(`{"thoughtSignature":"sig","x":1}`)}},
	}
	items := map[string]llm.Item{
		"user-string":     {Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}, TextContent: true},
		"user-parts":      {Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}, {Type: llm.PartImage, URL: "data:image/png;base64,AAA", Detail: "low"}}},
		"user-empty":      {Type: llm.ItemTypeMessage, Role: llm.RoleUser},
		"assistant-empty": {Type: llm.ItemTypeMessage, Role: llm.RoleAssistant},
		"assistant-text":  {Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "ok"}}, TextContent: true},
		"system":          {Type: llm.ItemTypeMessage, Role: llm.RoleSystem, Content: []llm.Part{{Type: llm.PartText, Text: "be brief"}}, TextContent: true},
		"reasoning":       {Type: llm.ItemTypeReasoning, Text: "thinking"},
		"tool-call":       {Type: llm.ItemTypeToolCall, CallID: "c1", Name: "f", Arguments: `{"a":"b"}`},
		"custom-call":     {Type: llm.ItemTypeToolCall, CallID: "c2", Name: "patch", Arguments: "*** Begin Patch", Custom: true},
		"tool-result":     {Type: llm.ItemTypeToolResult, CallID: "c1", Output: "done"},
		"native-item":     {Type: llm.ItemTypeNative},
		"unknown-item":    {Type: "bogus"},
		"assistant-after": {Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "after"}}},
	}
	var out []refCase
	add := func(name string, req llm.Request) { out = append(out, refCase{name: name, req: req}) }
	for name, it := range items {
		for _, k := range kept {
			it := it
			it.Origin = k.o
			add("item/"+name+"/"+k.name, llm.Request{Model: "m", Items: []llm.Item{it}})
			add("after-call/"+name+"/"+k.name, llm.Request{Model: "m", Items: []llm.Item{
				{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "f", Arguments: "{}"},
				it,
			}})
		}
	}
	parts := []struct {
		name string
		p    llm.Part
	}{
		{"image", llm.Part{Type: llm.PartImage, URL: "https://e/a.png", Detail: "high"}},
		{"image-by-id", llm.Part{Type: llm.PartImage, FileID: "f1"}},
		{"file-data", llm.Part{Type: llm.PartFile, FileData: "data:text/plain;base64,AA", Filename: "a.txt"}},
		{"file-by-url", llm.Part{Type: llm.PartFile, URL: "https://e/a.txt"}},
		{"file-empty", llm.Part{Type: llm.PartFile}},
		{"native-same", llm.Part{Type: llm.PartNative, Format: format, Data: json.RawMessage(`{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}`)}},
		{"native-other", llm.Part{Type: llm.PartNative, Format: llm.FormatAnthropicMessages, Data: json.RawMessage(`{"type":"text","text":"x"}`)}},
		{"unknown", llm.Part{Type: "bogus"}},
	}
	for _, tc := range texts {
		for _, pc := range parts {
			part := pc.p
			part.Origin = &llm.Origin{Format: format, Path: "p", Raw: json.RawMessage(`{"x_part":1}`)}
			add("part/"+tc.name+"/"+pc.name, llm.Request{Model: "m", Items: []llm.Item{
				{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: tc.text}, part}},
			}})
		}
		for _, role := range []string{llm.RoleSystem, llm.RoleUser, llm.RoleAssistant} {
			add("text/"+tc.name+"/"+role+"/string", llm.Request{Model: tc.text, Items: []llm.Item{
				{Type: llm.ItemTypeMessage, Role: role, Content: []llm.Part{{Type: llm.PartText, Text: tc.text}}, TextContent: true},
				{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "ok"}}, TextContent: true},
			}})
			add("text/"+tc.name+"/"+role+"/parts", llm.Request{Model: tc.text, Items: []llm.Item{
				{Type: llm.ItemTypeMessage, Role: role, Content: []llm.Part{{Type: llm.PartText, Text: tc.text}}},
				{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "ok"}}},
			}})
		}
		add("text/"+tc.name+"/call", llm.Request{Model: "m", Items: []llm.Item{
			{Type: llm.ItemTypeReasoning, Text: tc.text},
			{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "f", Arguments: tc.text},
		}})
		add("text/"+tc.name+"/custom-call", llm.Request{Model: "m", Items: []llm.Item{
			{Type: llm.ItemTypeToolCall, CallID: "c1", Name: "patch", Arguments: tc.text, Custom: true},
		}})
		add("text/"+tc.name+"/tool-result", llm.Request{Model: "m", Items: []llm.Item{
			{Type: llm.ItemTypeToolResult, CallID: "c1", Output: tc.text},
		}})
	}
	schemas := []struct{ name, schema string }{
		{"none", ""},
		{"object", `{"type":"object"}`},
		{"nested", `{"type":"object","properties":{"a":{"type":"array","items":{"type":"object","properties":{"b":{"type":"string","enum":["x","y"]}}}}}}`},
		{"spaced", `{ "type" : "object" , "properties" : { "a" : { "type" : "string" } } }`},
		{"invalid", `{"type":`},
	}
	for _, s := range schemas {
		for _, k := range kept {
			for _, st := range []*bool{nil, &strict, &notStrict} {
				tool := llm.Tool{Name: "f", Description: "does f", Strict: st, Origin: k.o}
				if s.schema != "" {
					tool.Parameters = json.RawMessage(s.schema)
				}
				add("tool/"+s.name+"/"+k.name, llm.Request{Model: "m", Tools: []llm.Tool{tool},
					Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}}})
			}
		}
	}
	add("tool/native-same", llm.Request{Model: "m", Tools: []llm.Tool{
		{Type: llm.ToolNative, Origin: &llm.Origin{Format: format, Path: "t", Raw: json.RawMessage(`{ "type" : "custom" , "custom" : { "name" : "patch" } }`)}},
	}})
	add("tool/native-other", llm.Request{Model: "m", Tools: []llm.Tool{
		{Type: llm.ToolNative, Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Path: "t", Raw: json.RawMessage(`{"type":"web_search"}`)}},
	}})
	efforts := []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "max", "auto", "turbo"}
	choice := []*llm.ToolChoice{nil,
		{Mode: llm.ToolChoiceAuto}, {Mode: llm.ToolChoiceNone}, {Mode: llm.ToolChoiceRequired},
		{Mode: llm.ToolChoiceTool, Name: "f"},
	}
	formats := []*llm.ResponseFormat{nil,
		{Type: llm.ResponseFormatText}, {Type: llm.ResponseFormatJSONObject},
		{Type: llm.ResponseFormatJSONSchema, Name: "r", Description: "d", Schema: json.RawMessage(`{"type":"object"}`), Strict: &strict},
	}
	base := []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}, TextContent: true},
	}
	for _, effort := range efforts {
		for ci, tc := range choice {
			for _, stream := range []bool{false, true} {
				req := llm.Request{Model: "m", Stream: stream, Items: base, Options: llm.Options{
					ReasoningEffort: effort, ToolChoice: tc, ParallelToolCalls: &strict,
					MaxOutputTokens: ptr(64), Temperature: ptrF(0.7), TopP: ptrF(0.9), Stop: []string{"END", "STOP"},
				}}
				if tc != nil {
					req.Tools = []llm.Tool{{Name: "f", Parameters: json.RawMessage(`{"type":"object"}`)}}
				}
				add(fmt.Sprintf("options/%s/choice%d/stream%v", effort, ci, stream), req)
			}
		}
	}
	for fi, rf := range formats {
		for _, stream := range []bool{false, true} {
			req := llm.Request{Model: "m", Stream: stream, Items: base, Options: llm.Options{ResponseFormat: rf}}
			add(fmt.Sprintf("format/%d/stream%v", fi, stream), req)
		}
	}
	for _, k := range kept {
		req := llm.Request{Model: "m", Items: base, Origin: k.o}
		add("origin/"+k.name, req)
		req2 := llm.Request{Model: "m", Items: base, Options: llm.Options{MaxOutputTokens: ptr(5)}, Origin: k.o}
		add("origin-options/"+k.name, req2)
	}
	add("broken-part", llm.Request{Model: "m", Items: []llm.Item{
		{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{
			{Type: llm.PartText, Text: "hi"},
			{Type: llm.PartNative, Format: format, Data: json.RawMessage(`{"type":`)},
		}},
		{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "ok"}}},
	}})
	return out
}
