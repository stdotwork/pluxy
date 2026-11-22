package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// inputOf returns the input items of a shaped request body, each as a map.
func inputOf(t *testing.T, m map[string]json.RawMessage) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal(m["input"], &items); err != nil {
		t.Fatalf("input: %v (%s)", err, m["input"])
	}
	return items
}

func typesOf(items []map[string]any) []string {
	var out []string
	for _, it := range items {
		typ, _ := it["type"].(string)
		if _, ok := it["role"]; ok && typ == "" {
			typ = "message"
		}
		out = append(out, typ)
	}
	return out
}

// lossAt returns the loss reported at path, if any.
func lossAt(tr *llm.Translation, path string) (llm.Loss, bool) {
	for _, l := range tr.Losses {
		if l.Path == path {
			return l, true
		}
	}
	return llm.Loss{}, false
}

// wantInterrupted is the output sent for a tool call that has none, as the Claude repairs word it.
const wantInterrupted = "Tool call was interrupted before any output was recorded."

// A tool call the conversation has no output for gets an interrupted output right after it, which
// Codex needs ("No tool output found for function call"), and each is reported. Calls that have
// their output, wherever it is, are left alone.
func TestMissingToolOutputs(t *testing.T) {
	body := `{"model":"gpt-6.1-sol","input":[
		{"role":"user","content":"go"},
		{"type":"function_call","call_id":"call_a","name":"read","arguments":"{}"},
		{"type":"function_call","call_id":"call_b","name":"read","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_b","output":"done"},
		{"type":"custom_tool_call","call_id":"call_c","name":"apply_patch","input":"x"},
		{"type":"tool_search_call","call_id":"call_d","arguments":{}},
		{"role":"user","content":"next"}]}`
	ur, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, account{subscription: true})
	items := inputOf(t, m)
	want := []string{"message", "function_call", "function_call_output", "function_call", "function_call_output",
		"custom_tool_call", "custom_tool_call_output", "tool_search_call", "tool_search_output", "message"}
	if got := typesOf(items); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("input types %v, want %v", got, want)
	}
	if items[2]["call_id"] != "call_a" || items[2]["output"] != wantInterrupted {
		t.Errorf("synthesized output for call_a: %v", items[2])
	}
	if items[4]["call_id"] != "call_b" || items[4]["output"] != "done" {
		t.Errorf("call_b's own output changed: %v", items[4])
	}
	if items[6]["call_id"] != "call_c" || items[6]["output"] != wantInterrupted {
		t.Errorf("synthesized output for call_c: %v", items[6])
	}
	if items[8]["call_id"] != "call_d" || items[8]["execution"] != "client" || items[8]["status"] != "completed" {
		t.Errorf("synthesized output for call_d: %v", items[8])
	}
	for _, p := range []string{"input[1]", "input[4]", "input[5]"} {
		if l, ok := lossAt(ur.tr, p); !ok || l.Kind != llm.LossApproximated {
			t.Errorf("no approximated loss at %s: %+v", p, ur.tr.Losses)
		}
	}
	if _, ok := lossAt(ur.tr, "input[2]"); ok {
		t.Errorf("an answered call was reported: %+v", ur.tr.Losses)
	}

	// Nothing to repair: the body goes as it came, with no loss.
	ur, m = shapeWith(t, responsesReq("gpt-6.1-sol", `{"model":"gpt-6.1-sol","input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`, nil), settings{}, account{subscription: true})
	if n := len(inputOf(t, m)); n != 2 || len(ur.tr.Losses) != 0 {
		t.Errorf("a paired conversation changed: %d items, losses %+v", n, ur.tr.Losses)
	}
}

// The Responses API answers a local_shell_call with a function_call_output or a
// local_shell_call_output. Either is an answer: no interrupted output is made beside it. A local
// shell call with neither still gets one.
func TestLocalShellCallAnswers(t *testing.T) {
	call := func(id string) string {
		return `{"type":"local_shell_call","call_id":"` + id + `","status":"completed","action":{"type":"exec","command":["ls"]}}`
	}
	body := `{"model":"gpt-6.1-sol","input":[{"role":"user","content":"go"},` +
		call("c1") + `,{"type":"local_shell_call_output","call_id":"c1","output":"{}"},` +
		call("c2") + `,{"type":"function_call_output","call_id":"c2","output":"ok"},` +
		call("c3") + `,{"role":"user","content":"next"}]}`
	ur, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, account{subscription: true})
	items := inputOf(t, m)
	want := []string{"message", "local_shell_call", "local_shell_call_output", "local_shell_call", "function_call_output",
		"local_shell_call", "function_call_output", "message"}
	if got := typesOf(items); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("input types %v, want %v", got, want)
	}
	if items[2]["output"] != "{}" || items[4]["output"] != "ok" {
		t.Errorf("a real output changed: %v, %v", items[2], items[4])
	}
	if items[6]["call_id"] != "c3" || items[6]["output"] != wantInterrupted {
		t.Errorf("synthesized output for c3: %v", items[6])
	}
	if len(ur.tr.Losses) != 1 {
		t.Errorf("want one loss, for c3 alone: %+v", ur.tr.Losses)
	}
	if l, ok := lossAt(ur.tr, "input[5]"); !ok || l.Kind != llm.LossApproximated {
		t.Errorf("no approximated loss at input[5]: %+v", ur.tr.Losses)
	}
}

// Claude Code on a Codex model: a tool_use its session stopped before answering (the next turn
// has no tool_result for it) reaches Codex with an interrupted output, not as a call Codex refuses.
func TestMessagesToolUseWithoutResultOnCodex(t *testing.T) {
	body := `{"model":"gpt-6.1-sol","max_tokens":1024,"messages":[
		{"role":"user","content":"list files"},
		{"role":"assistant","content":[{"type":"text","text":"Listing."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"text","text":"never mind, say hi"}]}]}`
	req, err := anthropicmessages.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	ur, m := shapeWith(t, req, settings{}, account{subscription: true})
	items := inputOf(t, m)
	var call, out int = -1, -1
	for i, it := range items {
		switch it["type"] {
		case "function_call":
			call = i
		case "function_call_output":
			out = i
		}
	}
	if call < 0 || out != call+1 || items[out]["call_id"] != items[call]["call_id"] || items[out]["output"] != wantInterrupted {
		t.Fatalf("input %v: want the call followed by its interrupted output", items)
	}
	if _, ok := lossAt(ur.tr, "input["+strconv.Itoa(call)+"]"); !ok {
		t.Errorf("the synthesized output is not reported: %+v", ur.tr.Losses)
	}
}

// Codex never stores a response (store is always false), so an item_reference in input can name
// nothing it has: over HTTP it is dropped and reported. Over the WebSocket, where a turn continues
// the socket's previous response, it is left for the upstream.
func TestItemReferences(t *testing.T) {
	body := `{"model":"gpt-6.1-sol","input":[{"type":"item_reference","id":"rs_1"},{"role":"user","content":"hi"}]}`
	ur, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, account{subscription: true})
	if got := typesOf(inputOf(t, m)); strings.Join(got, ",") != "message" {
		t.Errorf("HTTP input types %v, want the reference dropped", got)
	}
	if l, ok := lossAt(ur.tr, "input[0]"); !ok || l.Kind != llm.LossDropped {
		t.Errorf("the dropped reference is not reported: %+v", ur.tr.Losses)
	}

	ws, err := buildRequest(responsesReq("gpt-6.1-sol", body, nil), "acct", settings{}, account{subscription: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	var wm map[string]json.RawMessage
	_ = json.Unmarshal(ws.body, &wm)
	if got := typesOf(inputOf(t, wm)); strings.Join(got, ",") != "item_reference,message" {
		t.Errorf("WebSocket input types %v, want the reference kept", got)
	}

	// A client that asked for refusal over loss gets it.
	req := responsesReq("gpt-6.1-sol", body, nil)
	req.DropPolicy = llm.DropReject
	if _, err := buildRequest(req, "acct", settings{}, account{subscription: true}, false); err == nil {
		t.Error("drop_policy reject: the reference was dropped without an error")
	}
}

// agent_message items, which the Codex client sends for its subagents, go to a subscription as they
// came. A SIWC account refuses them (subscription_sharing_unsupported_capability), and every Codex
// upstream refuses one without its author or recipient: those are sent as user messages, with
// encrypted parts, which no one but the upstream that made them can read, dropped and reported.
func TestAgentMessages(t *testing.T) {
	body := `{"model":"gpt-6.1-sol","input":[
		{"type":"agent_message","author":"/root","recipient":"/root/probe","content":[{"type":"input_text","text":"Reply OK."},{"type":"encrypted_content","encrypted_content":"gAAAAAsecret"}]},
		{"type":"agent_message","recipient":"/root","content":[{"type":"input_text","text":"done"}]},
		{"type":"agent_message","author":"/root","recipient":"/root/x","content":[{"type":"encrypted_content","encrypted_content":"gAAAAAonly"}]}]}`
	_, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, account{subscription: true})
	items := inputOf(t, m)
	if got := typesOf(items); strings.Join(got, ",") != "agent_message,message,agent_message" {
		t.Errorf("subscription input types %v", got)
	}
	if items[0]["author"] != "/root" || len(items[0]["content"].([]any)) != 2 {
		t.Errorf("subscription: a complete agent_message changed: %v", items[0])
	}

	ur, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, account{siwc: true})
	items = inputOf(t, m)
	if got := typesOf(items); strings.Join(got, ",") != "message,message" {
		t.Fatalf("SIWC input types %v, want two user messages and the encrypted-only item dropped", got)
	}
	for _, it := range items {
		if it["role"] != "user" || it["author"] != nil || it["recipient"] != nil {
			t.Errorf("SIWC: %v, want a user message without author or recipient", it)
		}
	}
	parts, _ := items[0]["content"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Reply OK." {
		t.Errorf("SIWC: content %v, want the text part only", parts)
	}
	for path, kind := range map[string]string{"input[0]": llm.LossApproximated, "input[0].content[1]": llm.LossDropped, "input[2]": llm.LossDropped} {
		if l, ok := lossAt(ur.tr, path); !ok || l.Kind != kind {
			t.Errorf("no %s loss at %s: %+v", kind, path, ur.tr.Losses)
		}
	}
}

// An assistant message's phase (commentary or final_answer), which Codex takes on every account and
// asks clients to send back, reaches the upstream even when history repairs rewrite the input, and
// a Codex reply carrying those phases is reported as opaque output for non-Responses clients.
func TestPhaseReachesCodex(t *testing.T) {
	body := `{"model":"gpt-6.1-sol","input":[{"role":"user","content":"go"},
		{"type":"item_reference","id":"rs_stored"},
		{"type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Looking."}]},
		{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]},
		{"role":"user","content":"again"}]}`
	for _, a := range []account{{subscription: true}, {siwc: true}, {}} {
		ur, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, a)
		items := inputOf(t, m)
		if got := typesOf(items); strings.Join(got, ",") != "message,message,message,message" {
			t.Fatalf("account %+v: input types %v, want item_reference dropped", a, got)
		}
		if l, ok := lossAt(ur.tr, "input[1]"); !ok || l.Kind != llm.LossDropped {
			t.Errorf("account %+v: item_reference drop not reported: %+v", a, ur.tr.Losses)
		}
		if items[1]["phase"] != "commentary" || items[2]["phase"] != "final_answer" {
			t.Errorf("account %+v: phases %v and %v", a, items[1]["phase"], items[2]["phase"])
		}
	}

	reply := `{"type":"response.completed","response":{"id":"r1","object":"response","model":"gpt-6.1-sol","status":"completed","output":[` +
		`{"id":"msg_1","type":"message","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Looking."}]},` +
		`{"id":"msg_2","type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Done."}]}]}}`
	out, err := newRepairs().apply([]byte(reply))
	if err != nil {
		t.Fatal(err)
	}
	if got := openairesponses.OpaqueOutput(json.RawMessage(out)); !reflect.DeepEqual(got, []string{"output[0].phase", "output[1].phase"}) {
		t.Errorf("OpaqueOutput(relayed reply) = %q, want both phases reported", got)
	}
}

// A reasoning item whose ID is over Codex's limit cannot be sent, and its encrypted content cannot
// be read under another ID: it is left out, and that is reported.
func TestLongReasoningIDIsReported(t *testing.T) {
	body := `{"model":"gpt-6.1-sol","input":[{"type":"reasoning","id":"rs_` + strings.Repeat("a", 80) + `","summary":[],"encrypted_content":"gAAAAAx"},{"role":"user","content":"hi"}]}`
	ur, m := shapeWith(t, responsesReq("gpt-6.1-sol", body, nil), settings{}, account{subscription: true})
	if got := typesOf(inputOf(t, m)); strings.Join(got, ",") != "message" {
		t.Errorf("input types %v", got)
	}
	if l, ok := lossAt(ur.tr, "input[0]"); !ok || l.Kind != llm.LossDropped {
		t.Errorf("the left-out reasoning is not reported: %+v", ur.tr.Losses)
	}
}

const foreignReasoningRefusal = `{"error":{"message":"The encrypted content gAAA...Vw== could not be verified. Reason: Encrypted content could not be decrypted or parsed.","type":"invalid_request_error","param":null,"code":"invalid_encrypted_content"}}`

// reasoningUpstream answers its first n requests with refusal (status 400) and the rest with a
// short text stream, and keeps every request body.
type reasoningUpstream struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newReasoningUpstream(t *testing.T, refusals int, refusal string) *reasoningUpstream {
	t.Helper()
	u := &reasoningUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &b)
		u.mu.Lock()
		u.bodies = append(u.bodies, b)
		n := len(u.bodies)
		u.mu.Unlock()
		if n <= refusals {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, refusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		resp := `{"id":"resp_1","object":"response","model":"gpt-6.1-sol","status":"completed","output":[]}`
		io.WriteString(w, "data: "+`{"type":"response.created","response":`+resp+"}\n\n")
		io.WriteString(w, "data: "+`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`+"\n\n")
		io.WriteString(w, "data: "+`{"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"OK"}`+"\n\n")
		io.WriteString(w, "data: "+`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"OK"}]}}`+"\n\n")
		io.WriteString(w, "data: "+`{"type":"response.completed","response":`+resp+"}\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *reasoningUpstream) requests() []map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]map[string]any(nil), u.bodies...)
}

// replayBody is a Responses client's next turn, replaying reasoning another account produced.
const replayBody = `{"model":"gpt-6.1-sol","input":[{"role":"user","content":"hi"},
	{"type":"reasoning","id":"rs_other","summary":[],"encrypted_content":"gAAAAAforeign"},
	{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]},
	{"role":"user","content":"again"}]}`

func hasReasoning(body map[string]any) bool {
	items, _ := body["input"].([]any)
	for _, it := range items {
		if m, _ := it.(map[string]any); m["type"] == "reasoning" {
			return true
		}
	}
	return false
}

// Through the real extension program: Codex refuses the replayed reasoning of another account
// (invalid_encrypted_content); the extension sends the request once more without the reasoning
// items, the client gets the reply, and the reply's translation report names what was left out.
func TestForeignReasoningRetryThroughTheExtension(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	u := newReasoningUpstream(t, 1, foreignReasoningRefusal)
	settingsJSON, _ := json.Marshal(map[string]any{"base_url": u.URL + "/backend-api/codex"})
	acct := protocol.Account{ID: "codex", Settings: settingsJSON, Credential: subscription("tok", "")}

	s := ext.Generate(context.Background(), acct, responsesReq("gpt-6.1-sol", replayBody, nil))
	defer s.Close()
	var start *llm.ResponseInfo
	text := ""
	for {
		ev := s.Next()
		if ev.Type == llm.EventError {
			t.Fatalf("the call failed: %+v", ev.Error)
		}
		if ev.Type == llm.EventResponseStart {
			start = ev.Response
		}
		if ev.Type == llm.EventTextDelta {
			text += ev.Delta
		}
		if ev.Terminal() {
			break
		}
	}
	reqs := u.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d upstream requests, want the refused one and one retry", len(reqs))
	}
	if !hasReasoning(reqs[0]) || hasReasoning(reqs[1]) {
		t.Errorf("reasoning in the first request %v, in the retry %v; want it only in the first", hasReasoning(reqs[0]), hasReasoning(reqs[1]))
	}
	if n := len(reqs[1]["input"].([]any)); n != 3 {
		t.Errorf("the retry has %d input items, want the 3 that are not reasoning", n)
	}
	if text != "OK" {
		t.Errorf("client got text %q", text)
	}
	if start == nil || start.Translation == nil {
		t.Fatalf("no translation report on response.start: %+v", start)
	}
	if l, ok := lossAt(start.Translation, "input[1]"); !ok || l.Kind != llm.LossDropped {
		t.Errorf("the left-out reasoning is not reported: %+v", start.Translation.Losses)
	}
}

// The retry happens once, only for that refusal, only when there is reasoning to leave out, and
// not for a client that asked for refusal over loss.
func TestForeignReasoningRetryLimits(t *testing.T) {
	call := func(u *reasoningUpstream, body string, policy string) error {
		settingsJSON, _ := json.Marshal(map[string]any{"base_url": u.URL})
		req := responsesReq("gpt-6.1-sol", body, nil)
		req.DropPolicy = policy
		return generate(context.Background(), sdk.Call{Account: protocol.Account{ID: "codex", Settings: settingsJSON, Credential: subscription("tok", "")}, Request: req}, nil)
	}
	u := newReasoningUpstream(t, 2, foreignReasoningRefusal)
	wantError(t, call(u, replayBody, ""), llm.CodeInvalidRequest, false)
	if n := len(u.requests()); n != 2 {
		t.Errorf("refused twice: %d requests, want 2", n)
	}
	u = newReasoningUpstream(t, 1, foreignReasoningRefusal)
	wantError(t, call(u, `{"model":"gpt-6.1-sol","input":"hi"}`, ""), llm.CodeInvalidRequest, false)
	if n := len(u.requests()); n != 1 {
		t.Errorf("no reasoning to leave out: %d requests, want 1", n)
	}
	u = newReasoningUpstream(t, 1, `{"error":{"message":"bad","type":"invalid_request_error","code":"invalid_value"}}`)
	wantError(t, call(u, replayBody, ""), llm.CodeInvalidRequest, false)
	if n := len(u.requests()); n != 1 {
		t.Errorf("another refusal: %d requests, want 1", n)
	}
	u = newReasoningUpstream(t, 1, foreignReasoningRefusal)
	wantError(t, call(u, replayBody, llm.DropReject), llm.CodeInvalidRequest, false)
	if n := len(u.requests()); n != 1 {
		t.Errorf("drop_policy reject: %d requests, want 1", n)
	}
}
