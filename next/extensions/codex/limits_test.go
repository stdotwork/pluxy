package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// sentBody builds the upstream request for req and returns it with its body read as JSON.
func sentBody(t *testing.T, req llm.Request) (*upstreamRequest, struct {
	Input      []map[string]any `json:"input"`
	Tools      []map[string]any `json:"tools"`
	ToolChoice map[string]any   `json:"tool_choice"`
}) {
	t.Helper()
	ur, err := buildRequest(req, "acct", settings{}, account{token: "sk-test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input      []map[string]any `json:"input"`
		Tools      []map[string]any `json:"tools"`
		ToolChoice map[string]any   `json:"tool_choice"`
	}
	if err := json.Unmarshal(ur.body, &body); err != nil {
		t.Fatalf("upstream body %s: %v", ur.body, err)
	}
	return ur, body
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func isPortable(s string) bool {
	for i := 0; i < len(s); i++ {
		if !portableByte(s[i]) {
			return false
		}
	}
	return true
}

// A request core translated (here with no native body, as from Chat, Messages or Gemini) goes
// upstream with function names the endpoint takes, the same name in the tools, the tool choice
// and the calls in the conversation, and call IDs within the limit that still pair a call with
// its result. The reply's names map back.
func TestTranslatedRequestNamesAndCallIDs(t *testing.T) {
	longA := "mcp__" + strings.Repeat("s", 70) + "__lookup_issue"
	longB := "mcp__" + strings.Repeat("t", 70) + "__lookup_issue"
	plainLong := strings.Repeat("p", 100)
	longCall := "call_" + strings.Repeat("c", 100)
	args := `{"q":1}`
	req := llm.Request{
		Model: "gpt-test",
		Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}},
			{Type: llm.ItemTypeToolCall, CallID: longCall, Name: longA, Arguments: args},
			{Type: llm.ItemTypeToolResult, CallID: longCall, Output: "ok"},
		},
		Tools: []llm.Tool{
			{Name: longA, Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: longB, Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "get.weather", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: plainLong, Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		Options: llm.Options{ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: longB}},
	}
	ur, body := sentBody(t, req)

	var tools []string
	for _, tool := range body.Tools {
		if tool["type"] == "function" {
			tools = append(tools, tool["name"].(string))
		}
	}
	want := []string{"mcp__lookup_issue", "mcp__lookup_issue_1", "get_weather", "read", strings.Repeat("p", 64)}
	if !slices.Equal(tools, want) {
		t.Errorf("tool names %q, want %q", tools, want)
	}
	if got := body.ToolChoice["name"]; got != "mcp__lookup_issue_1" {
		t.Errorf("tool choice names %v, want the name sent for the second tool", got)
	}
	var call, result map[string]any
	for _, it := range body.Input {
		switch it["type"] {
		case "function_call":
			call = it
		case "function_call_output":
			result = it
		}
	}
	if call["name"] != "mcp__lookup_issue" {
		t.Errorf("the call in the conversation is named %v, want the name sent for the first tool", call["name"])
	}
	wantCall := longCall[:47] + "_" + hashOf(longCall)
	if call["call_id"] != wantCall || result["call_id"] != wantCall {
		t.Errorf("call IDs %v and %v, want both %q", call["call_id"], result["call_id"], wantCall)
	}
	if len(wantCall) > 64 {
		t.Fatalf("test expectation %q is over the limit", wantCall)
	}
	wantNames := map[string]string{
		"mcp__lookup_issue": longA, "mcp__lookup_issue_1": longB, "get_weather": "get.weather", strings.Repeat("p", 64): plainLong,
	}
	if len(ur.names) != len(wantNames) {
		t.Errorf("names to give back %v, want %v", ur.names, wantNames)
	}
	for sent, original := range wantNames {
		if ur.names[sent] != original {
			t.Errorf("name %q maps back to %q, want %q", sent, ur.names[sent], original)
		}
	}
}

// A request that needs no change sends no names back to rewrite.
func TestShortNamesNeedNoMapping(t *testing.T) {
	req := llm.Request{Model: "gpt-test",
		Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Tools: []llm.Tool{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	ur, body := sentBody(t, req)
	if len(ur.names) != 0 {
		t.Errorf("names to give back %v, want none", ur.names)
	}
	if len(body.Tools) == 0 || body.Tools[0]["name"] != "read" {
		t.Errorf("tools %v, want read", body.Tools)
	}
}

// A call ID is cut at a character boundary, so the result stays within the limit.
func TestShortCallIDKeepsCharactersWhole(t *testing.T) {
	id := strings.Repeat("a", 46) + strings.Repeat("é", 30)
	got := shortCallID(id)
	if len(got) > callIDLimit || !utf8.ValidString(got) {
		t.Errorf("%q is %d bytes, valid UTF-8 %v", got, len(got), utf8.ValidString(got))
	}
	if short := strings.Repeat("a", 64); shortCallID(short) != short {
		t.Errorf("a call ID of exactly 64 bytes changed")
	}
}

// A Responses client's own body goes to the endpoint as it is, as today's core sends it: its
// function names and call IDs are not touched, and no name is given back in the reply.
func TestResponsesBodyKeepsNamesAndCallIDs(t *testing.T) {
	long := "mcp__" + strings.Repeat("s", 70) + "__lookup_issue"
	longCall := "call_" + strings.Repeat("c", 100)
	req, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-test","input":[` +
		`{"type":"function_call","call_id":"` + longCall + `","name":"` + long + `","arguments":"{}"},` +
		`{"type":"function_call_output","call_id":"` + longCall + `","output":"ok"}],` +
		`"tools":[{"type":"function","name":"` + long + `","parameters":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ur, body := sentBody(t, req)
	if len(ur.names) != 0 {
		t.Errorf("names to give back %v, want none", ur.names)
	}
	if body.Tools[0]["name"] != long || body.Input[0]["name"] != long {
		t.Errorf("names changed: %v, %v", body.Tools[0]["name"], body.Input[0]["name"])
	}
	if body.Input[0]["call_id"] != longCall || body.Input[1]["call_id"] != longCall {
		t.Errorf("call IDs changed: %v, %v", body.Input[0]["call_id"], body.Input[1]["call_id"])
	}
}

type collectedEvents struct{ events []llm.Event }

func (c *collectedEvents) Send(ev llm.Event) error {
	c.events = append(c.events, ev)
	return nil
}

// A tool call in the reply, streamed or not, names the function as the client did.
func TestReplyGivesBackClientNames(t *testing.T) {
	names := map[string]string{"mcp__lookup_issue": "mcp__server__lookup_issue"}
	call := `{"type":"function_call","call_id":"call_1","name":"mcp__lookup_issue","arguments":"{}"}`
	other := `{"type":"function_call","call_id":"call_2","name":"read","arguments":"{}"}`
	stream := "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":" + call + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + call + "}\n\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":" + other + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":" + other + "}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[]}}\n\n"
	whole := `{"id":"resp_1","status":"completed","output":[` + call + `,` + other + `]}`
	for name, resp := range map[string]*http.Response{
		"stream":   {Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))},
		"response": {Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(whole))},
	} {
		t.Run(name, func(t *testing.T) {
			out := &collectedEvents{}
			if err := relay(context.Background(), resp, renamer{out, names}, newRepairs()); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, ev := range out.events {
				if ev.Type == llm.EventItemStart {
					got = append(got, ev.Item.Name)
				}
			}
			if want := []string{"mcp__server__lookup_issue", "read"}; !slices.Equal(got, want) {
				t.Errorf("tool call names %q, want %q", got, want)
			}
		})
	}
}

// itemIDs builds the upstream request for a Responses body and returns its input items.
func itemIDs(t *testing.T, input string) []map[string]any {
	t.Helper()
	req, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-test","input":` + input + `}`))
	if err != nil {
		t.Fatal(err)
	}
	_, body := sentBody(t, req)
	return body.Input
}

func idOf(it map[string]any) string {
	id, _ := it["id"].(string)
	return id
}

// An input item ID is within 64 characters and holds only ASCII letters, digits, _ and -, with the
// prefix of its item type; what is already right stays as it is.
func TestInputItemIDs(t *testing.T) {
	id64 := strings.Repeat("a", 64)
	id65 := strings.Repeat("b", 65)
	unicode65 := strings.Repeat("界", 65)
	got := itemIDs(t, `[`+
		`{"type":"function_call_output","id":"`+id64+`","call_id":"c0","output":"ok"},`+
		`{"type":"function_call_output","id":"`+id65+`","call_id":"c0","output":"ok"},`+
		`{"type":"function_call_output","id":"`+unicode65+`","call_id":"c0","output":"ok"},`+
		`{"type":"message","role":"user","id":"item_74ec40c8","content":[]},`+
		`{"type":"message","role":"assistant","id":"msg-1","content":[]},`+
		`{"type":"reasoning","id":"item_reasoning","summary":[]},`+
		`{"type":"function_call","id":"call:0+1/2","call_id":"c1","name":"f","arguments":"{}"},`+
		`{"type":"function_call_output","id":"fco:0+1/2","call_id":"c1","output":"ok"},`+
		`{"type":"custom_tool_call","id":"item_custom","call_id":"c2","name":"f","input":"x"},`+
		`{"type":"custom_tool_call_output","id":"item_custom_out","call_id":"c2","output":"x"},`+
		`{"type":"tool_search_call","id":"ts_call_4000860","call_id":"c3","execution":"client"}]`)
	want := []string{
		id64,
		strings.Repeat("b", 47) + "_" + hashOf(id65),
		strings.Repeat("_", 47) + "_" + hashOf(unicode65),
		"msg_item_74ec40c8",
		"msg-1",
		"rs_item_reasoning",
		"fc_call_0_1_2",
		"fco_0_1_2",
		"ctc_item_custom",
		"ctco_item_custom_out",
		"tsc_call_4000860",
	}
	if len(got) != len(want) {
		t.Fatalf("%d input items, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if idOf(got[i]) != w {
			t.Errorf("item %d ID %q, want %q", i, idOf(got[i]), w)
		}
	}
	for i, it := range got {
		if n := utf8.RuneCountInString(idOf(it)); n > 64 || !isPortable(idOf(it)) {
			t.Errorf("item %d ID %q: %d characters, portable %v", i, idOf(it), n, isPortable(idOf(it)))
		}
	}
}

// Two different IDs never come out the same, an ID that was right never changes to make room, and
// the same ID with the same type comes out the same each time.
func TestInputItemIDsStayDistinct(t *testing.T) {
	msg := func(id string) string { return `{"type":"message","role":"user","id":"` + id + `","content":[]}` }
	out := func(id string) string {
		return `{"type":"function_call_output","id":"` + id + `","call_id":"c","output":"ok"}`
	}
	long := strings.Repeat("grok-item-", 10)
	cut := strings.Repeat("grok-item-", 4) + "grok-it" + "_" + hashOf(long) // what long is cut to
	for name, tc := range map[string]struct {
		in       string
		keepIdx  int // the item whose ID was already right
		keepWant string
	}{
		"prefixed ID last":  {`[` + msg("item_x") + `,` + msg("msg_item_x") + `]`, 1, "msg_item_x"},
		"prefixed ID first": {`[` + msg("msg_item_x") + `,` + msg("item_x") + `]`, 0, "msg_item_x"},
		"cut ID taken":      {`[` + out(long) + `,` + out(cut) + `]`, 1, cut},
		"cleaned ID taken":  {`[` + msg("msg_a+b") + `,` + msg("msg_a_b") + `]`, 1, "msg_a_b"},
	} {
		t.Run(name, func(t *testing.T) {
			got := itemIDs(t, tc.in)
			if idOf(got[0]) == idOf(got[1]) {
				t.Fatalf("both items have ID %q", idOf(got[0]))
			}
			if idOf(got[tc.keepIdx]) != tc.keepWant {
				t.Errorf("the right ID became %q, want %q", idOf(got[tc.keepIdx]), tc.keepWant)
			}
			for _, it := range got {
				if n := utf8.RuneCountInString(idOf(it)); n > 64 {
					t.Errorf("ID %q has %d characters", idOf(it), n)
				}
			}
			// What was sent goes through again unchanged.
			again, _ := json.Marshal(got)
			if second := itemIDs(t, string(again)); !slices.EqualFunc(second, got, func(a, b map[string]any) bool { return idOf(a) == idOf(b) }) {
				t.Errorf("a second pass changed the IDs: %v then %v", got, second)
			}
		})
	}
	same := itemIDs(t, `[`+msg("msg_a+b")+`,`+msg("msg_a+b")+`]`)
	if idOf(same[0]) != idOf(same[1]) || idOf(same[0]) != "msg_a_b" {
		t.Errorf("one ID twice came out %q and %q, want msg_a_b both", idOf(same[0]), idOf(same[1]))
	}
}

// A reasoning item with encrypted content and an ID over the limit is left out; one with a short
// ID, or without encrypted content, is kept.
func TestOverlongEncryptedReasoningIsDropped(t *testing.T) {
	long := "rs_" + strings.Repeat("a", 64)
	short := "rs_" + strings.Repeat("b", 48)
	got := itemIDs(t, `[`+
		`{"type":"message","role":"user","id":"msg-1","content":[]},`+
		`{"type":"reasoning","id":"`+long+`","encrypted_content":"gAAAA","summary":[]},`+
		`{"type":"reasoning","id":"`+short+`","encrypted_content":"gAAAA","summary":[]},`+
		`{"type":"reasoning","id":"`+long+`x","encrypted_content":"","summary":[]},`+
		`{"type":"reasoning","id":"`+long+`y","summary":[]}]`)
	if len(got) != 4 {
		t.Fatalf("%d input items, want 4: %v", len(got), got)
	}
	if idOf(got[0]) != "msg-1" || idOf(got[1]) != short {
		t.Errorf("IDs %q, %q, want msg-1 and %q", idOf(got[0]), idOf(got[1]), short)
	}
	for _, it := range got[2:] {
		if n := utf8.RuneCountInString(idOf(it)); n != 64 {
			t.Errorf("reasoning ID %q has %d characters, want it cut to 64", idOf(it), n)
		}
	}
}
