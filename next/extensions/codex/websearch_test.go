package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	webSearchItem = `{"id":"ws_1","type":"web_search_call","status":"completed","action":{"type":"search","query":"q"}}`
	userMsg       = `{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`
)

// shapedBody builds the upstream body for a Responses request, as /responses (ws false) or the
// Responses WebSocket (ws true) sends it.
func shapedBody(t *testing.T, body string, headers map[string]string, ws bool) map[string]json.RawMessage {
	t.Helper()
	req := wsRequest(t, body)
	req.Native = &llm.Native{Format: llm.FormatOpenAIResponses, Body: json.RawMessage(body), Headers: headers}
	ur, err := buildRequest(req, "acct", settings{}, account{}, ws)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ur.body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func toolTypes(t *testing.T, m map[string]json.RawMessage) []string {
	t.Helper()
	var tools []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(m["tools"], &tools); err != nil && m["tools"] != nil {
		t.Fatalf("tools %s: %v", m["tools"], err)
	}
	var types []string
	for _, tl := range tools {
		types = append(types, tl.Type)
	}
	return types
}

func inputTypes(t *testing.T, m map[string]json.RawMessage) string {
	t.Helper()
	var items []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(m["input"], &items); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, it := range items {
		types = append(types, it.Type)
	}
	return strings.Join(types, ",")
}

// A request that replays a web search without declaring a web search tool gets one, over HTTP and
// the WebSocket alike, ahead of the image generation tool the extension adds.
func TestReplayedWebSearchGetsTool(t *testing.T) {
	for _, tools := range []string{`"tools":[],`, ``, `"tools":null,`, `"tools":[{"type":"function","name":"f","parameters":{}}],`} {
		for _, ws := range []bool{false, true} {
			m := shapedBody(t, `{"model":"m",`+tools+`"input":[`+userMsg+`,`+webSearchItem+`]}`, nil, ws)
			got := strings.Join(toolTypes(t, m), ",")
			want := "web_search,image_generation"
			if strings.Contains(tools, "function") {
				want = "function,web_search,image_generation"
			}
			if got != want {
				t.Errorf("%s ws=%v: tools %q, want %q", tools, ws, got, want)
			}
			if got := inputTypes(t, m); got != "message,web_search_call" {
				t.Errorf("%s ws=%v: input %q, the search call must stay", tools, ws, got)
			}
		}
	}
}

// A request that already declares a web search tool, or replays no search, is left as it is.
func TestWebSearchToolLeftAlone(t *testing.T) {
	for _, c := range []struct{ name, body, tools string }{
		{"web_search declared", `{"model":"m","tools":[{"type":"web_search","external_web_access":true}],"input":[` + webSearchItem + `]}`, "web_search,image_generation"},
		{"preview declared", `{"model":"m","tools":[{"type":"web_search_preview"}],"input":[` + webSearchItem + `]}`, "web_search_preview,image_generation"},
		{"dated preview declared", `{"model":"m","tools":[{"type":"web_search_preview_2025_03_11"}],"input":[` + webSearchItem + `]}`, "web_search_preview_2025_03_11,image_generation"},
		{"no search in input", `{"model":"m","input":[` + userMsg + `]}`, "image_generation"},
		{"search call nested in a message", `{"model":"m","input":[{"type":"message","role":"user","content":[` + webSearchItem + `]}]}`, "image_generation"},
		{"input is text", `{"model":"m","input":"web_search_call"}`, "image_generation"},
	} {
		m := shapedBody(t, c.body, nil, false)
		if got := strings.Join(toolTypes(t, m), ","); got != c.tools {
			t.Errorf("%s: tools %q, want %q", c.name, got, c.tools)
		}
	}
	m := shapedBody(t, `{"model":"m","tools":[{"type":"web_search","external_web_access":true}],"input":[`+webSearchItem+`]}`, nil, false)
	if !strings.Contains(string(m["tools"]), `"external_web_access":true`) {
		t.Errorf("the declared tool's options were lost: %s", m["tools"])
	}
}

// A Responses-lite request declares no hosted tools, so its replayed searches are removed instead
// and no tool is added.
func TestReplayedWebSearchDroppedForLite(t *testing.T) {
	lite := map[string]string{"x-openai-internal-codex-responses-lite": "true"}
	for _, ws := range []bool{false, true} {
		m := shapedBody(t, `{"model":"m","input":[`+userMsg+`,`+webSearchItem+`,{"type":"reasoning","summary":[]},`+webSearchItem+`]}`, lite, ws)
		if got := inputTypes(t, m); got != "message,reasoning" {
			t.Errorf("ws=%v: input %q, want the search calls removed", ws, got)
		}
		if _, ok := m["tools"]; ok {
			t.Errorf("ws=%v: lite request got tools %s", ws, m["tools"])
		}
	}
	// With a web search tool declared, nothing is removed.
	m := shapedBody(t, `{"model":"m","tools":[{"type":"web_search"}],"input":[`+userMsg+`,`+webSearchItem+`]}`, lite, false)
	if got := inputTypes(t, m); got != "message,web_search_call" {
		t.Errorf("lite request with a web search tool: input %q, want it kept", got)
	}
}

// The compact endpoint applies the same rule to a body it sends nearly as it came.
func TestWebSearchForHistoryOnABody(t *testing.T) {
	parse := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := parse(`{"input":[` + userMsg + `,` + webSearchItem + `,{"type":"compaction_trigger"}]}`)
	webSearchForHistory(m, false)
	if got := string(m["tools"]); got != `[{"type":"web_search"}]` {
		t.Errorf("tools %s, want one web_search tool", got)
	}
	m = parse(`{"input":[` + userMsg + `,` + webSearchItem + `,{"type":"compaction_trigger"}]}`)
	webSearchForHistory(m, true)
	if got := inputTypes(t, m); got != "message,compaction_trigger" {
		t.Errorf("lite: input %q", got)
	}
	m = parse(`{"model":"m"}`)
	webSearchForHistory(m, false)
	if _, ok := m["tools"]; ok || len(m) != 1 {
		t.Errorf("a body without input changed: %v", m)
	}
}
