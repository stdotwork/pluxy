package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A client's model name becomes Devin's model ID as today's core builds it: the request's effort
// or the model's default, moved to the nearest effort the model has.
func TestModelID(t *testing.T) {
	for _, c := range []struct{ model, effort, want string }{
		{"swe-2", "", "swe-2-high"},
		{"", "", "swe-2-high"},
		{"devin/claude-opus-5-5", "", "claude-opus-5-5-medium"},
		{"claude-opus-5-5", "xhigh", "claude-opus-5-5-xhigh"},
		{"claude-opus-5-5-high", "low", "claude-opus-5-5-high"},
		{"claude-opus-5-5", "adaptive", "claude-opus-5-5-high"},
		{"gpt-5-5", "", "gpt-5-5-low"},
		{"gemini-3-8-flash", "", "gemini-3-8-flash-high"},
		{"glm-5-2", "", "glm-5-2-none"},
		{"glm-5-2", "high", "glm-5-2-max"},
		{"kimi-k3", "medium", "kimi-k3-high"},
		{"swe-1-6", "high", "swe-1-6"},
		{"swe-2-lightning", "", "swe-2-high-lightning"},
		{"swe-1-6-slow", "", "swe-1-6-slow"},
		{"new-model", "", "new-model"},
		{"new-model", "high", "new-model-high"},
	} {
		if got := modelID(c.model, c.effort); got != c.want {
			t.Errorf("modelID(%q, %q) = %q, want %q", c.model, c.effort, got, c.want)
		}
	}
}

// fieldsOf decodes a message's fields by number, failing the test on a malformed message.
func fieldsOf(t *testing.T, b []byte) map[int][]field {
	t.Helper()
	fs, err := parseFields(b)
	if err != nil {
		t.Fatalf("parsing %x: %v", b, err)
	}
	out := map[int][]field{}
	for _, f := range fs {
		out[f.num] = append(out[f.num], f)
	}
	return out
}

func str(fs map[int][]field, n int) string {
	if len(fs[n]) == 0 {
		return ""
	}
	return string(fs[n][0].data)
}

// A conversation becomes Devin's turns: system text as the system prompt, the assistant's
// reasoning, text and calls in one turn, a result for a call as a tool turn and one for no call
// as the user's text; tools, output limit, temperature and model ID in their fields.
func TestRequestEncoding(t *testing.T) {
	max, temp := 100000, 0.3
	sig := "sealed.v1.sig"
	req := &llm.Request{Model: "claude-opus-5-5", Options: llm.Options{MaxOutputTokens: &max, Temperature: &temp, ReasoningEffort: "high"},
		Tools: []llm.Tool{{Type: llm.ToolFunction, Name: "weather", Description: "Get weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		Items: []llm.Item{
			{Type: llm.ItemTypeMessage, Role: llm.RoleSystem, Content: []llm.Part{{Type: llm.PartText, Text: "Be brief."}}},
			{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Look"}, {Type: llm.PartImage, URL: "data:image/jpeg;base64,QUJD"}}},
			{Type: llm.ItemTypeReasoning, Text: "thinking", Origin: &llm.Origin{Format: llm.FormatAnthropicMessages, Raw: json.RawMessage(`{"signature":"` + sig + `"}`)}},
			{Type: llm.ItemTypeMessage, Role: llm.RoleAssistant, Content: []llm.Part{{Type: llm.PartText, Text: "Checking."}}},
			{Type: llm.ItemTypeToolCall, CallID: "call_1", Name: "weather", Arguments: `{"city":"Paris"}`},
			{Type: llm.ItemTypeToolResult, CallID: "call_1", Output: "sunny"},
			{Type: llm.ItemTypeToolResult, CallID: "call_gone", Output: "late"},
		}}
	c := buildRequest(req)
	top := fieldsOf(t, c.encode("tok", "fp"))
	if got := str(top, 2); got != "Be brief." {
		t.Errorf("system prompt %q", got)
	}
	if got := str(top, 21); got != "claude-opus-5-5-high" {
		t.Errorf("model %q", got)
	}
	meta := fieldsOf(t, top[1][0].data)
	if str(meta, 1) != cliName || str(meta, 3) != "tok" || str(meta, 31) != "fp" {
		t.Errorf("client metadata name %q token %q fingerprint %q", str(meta, 1), str(meta, 3), str(meta, 31))
	}
	var sources []uint64
	for _, tf := range top[3] {
		sources = append(sources, fieldsOf(t, tf.data)[2][0].val)
	}
	if want := []uint64{1, 2, 4, 1}; !slicesEqual(sources, want) {
		t.Fatalf("turn sources %v, want %v", sources, want)
	}
	user := fieldsOf(t, top[3][0].data)
	img := fieldsOf(t, user[10][0].data)
	if str(user, 3) != "[Image 1: pasted_image_1.jpg]\n\nLook" || str(img, 1) != "QUJD" || str(img, 2) != "image/jpeg" {
		t.Errorf("user turn text %q image %q %q", str(user, 3), str(img, 1), str(img, 2))
	}
	asst := fieldsOf(t, top[3][1].data)
	call := fieldsOf(t, asst[6][0].data)
	if str(asst, 3) != "Checking." || str(asst, 11) != "thinking" || str(asst, 12) != "sealed.v1.sig" || str(asst, 18) != "sealed" || str(call, 1) != "call_1" || str(call, 3) != `{"city":"Paris"}` {
		t.Errorf("assistant turn text %q thinking %q signature %q call %q %q", str(asst, 3), str(asst, 11), str(asst, 12)+"/"+str(asst, 18), str(call, 1), str(call, 3))
	}
	if tool := fieldsOf(t, top[3][2].data); str(tool, 7) != "call_1" || str(tool, 3) != "sunny" {
		t.Errorf("tool turn answers %q with %q", str(tool, 7), str(tool, 3))
	}
	if orphan := fieldsOf(t, top[3][3].data); str(orphan, 3) != "late" || len(orphan[7]) != 0 {
		t.Errorf("result for no call: text %q, call %q", str(orphan, 3), str(orphan, 7))
	}
	cfg := fieldsOf(t, top[8][0].data)
	if cfg[2][0].val != maxOutputTokens || cfg[5][0].double() != 0.3 || cfg[8][0].double() != float64(float32(0.95)) {
		t.Errorf("completion settings max %d temperature %v top_p %v", cfg[2][0].val, cfg[5][0].double(), math.Float64frombits(cfg[8][0].val))
	}
	tool := fieldsOf(t, top[10][0].data)
	if str(tool, 1) != "weather" || str(tool, 2) != "Get weather" || str(tool, 3) != `{"type":"object"}` {
		t.Errorf("tool %q %q %q", str(tool, 1), str(tool, 2), str(tool, 3))
	}
	sess := fieldsOf(t, top[15][0].data)
	if str(sess, 1) == "" || str(sess, 1) != str(top, 16) || len(sess[4]) != 1 {
		t.Errorf("session %q, cascade %q, user-turn marker %v; want the same ID and the marker", str(sess, 1), str(top, 16), sess[4])
	}
}

func slicesEqual(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type recorder struct{ events []llm.Event }

func (r *recorder) Send(ev llm.Event) error {
	r.events = append(r.events, ev)
	return nil
}

func (r *recorder) types() string {
	var s []string
	for _, ev := range r.events {
		s = append(s, ev.Type)
	}
	return strings.Join(s, " ")
}

func relayFrames(frames ...[]byte) (*recorder, error) {
	rec := &recorder{}
	r := &relay{out: rec, model: "swe-2"}
	return rec, r.run(context.Background(), bytes.NewReader(bytes.Join(frames, nil)))
}

// A reply's frames become items as they arrive: reasoning with its signature, text, and each
// tool call by its ID, then the finish with usage.
func TestRelay(t *testing.T) {
	sig := devintest.Frame(devintest.Bytes(nil, 10, []byte("sig")))
	rec, err := relayFrames(devintest.Thinking("Hmm"), sig, devintest.Text("Sure."), devintest.ToolCall("call_a", "weather", `{"ci`),
		devintest.ToolCall("", "", `ty":"Paris"}`), devintest.ToolCall("call_b", "time", `{}`), devintest.Usage(11, 7), devintest.End(""))
	if err != nil {
		t.Fatal(err)
	}
	want := "response.start item.start reasoning.delta item.end item.start text.delta item.end item.start tool_input.delta tool_input.delta item.end item.start tool_input.delta item.end finish"
	if got := rec.types(); got != want {
		t.Fatalf("events\n %s\nwant\n %s", got, want)
	}
	ev := rec.events
	if ev[3].Opaque == nil || ev[3].Opaque.Data != "sig" {
		t.Errorf("reasoning end opaque %+v, want the signature", ev[3].Opaque)
	}
	if it := ev[7].Item; it.CallID != "call_a" || it.Name != "weather" || ev[8].Delta+ev[9].Delta != `{"city":"Paris"}` {
		t.Errorf("first call %+v with arguments %q", it, ev[8].Delta+ev[9].Delta)
	}
	if it := ev[11].Item; it.CallID != "call_b" || it.Name != "time" {
		t.Errorf("second call %+v", it)
	}
	fin := ev[len(ev)-1]
	if fin.Finish.Reason != llm.FinishToolCalls || *fin.Usage.InputTokens != 11 || *fin.Usage.OutputTokens != 7 {
		t.Errorf("finish %+v usage %+v", fin.Finish, fin.Usage)
	}
}

// An end frame's error is classified as today's core maps Connect codes; one after output is not
// retry-safe; a stream that ends without an end frame is an error, and a garbled end frame a
// clean end.
func TestReplyErrors(t *testing.T) {
	for _, c := range []struct {
		name      string
		frames    [][]byte
		code      string
		scope     string
		retryable bool
	}{
		{"quota used up", [][]byte{devintest.End("resource_exhausted", "daily quota exhausted")}, llm.CodeQuota, llm.ScopeCredential, true},
		{"rate limit", [][]byte{devintest.End("resource_exhausted", "slow down")}, llm.CodeRateLimit, llm.ScopeModel, true},
		{"high demand", [][]byte{devintest.End("permission_denied", "model under high demand")}, llm.CodeRateLimit, llm.ScopeModel, true},
		{"credits", [][]byte{devintest.End("failed_precondition", "out of credits")}, llm.CodeRateLimit, llm.ScopeModel, true},
		{"refused token", [][]byte{devintest.End("unauthenticated", "bad token")}, llm.CodeAuth, llm.ScopeCredential, true},
		{"bad request", [][]byte{devintest.End("invalid_argument", "bad field")}, llm.CodeInvalidRequest, llm.ScopeRequest, false},
		{"after output", [][]byte{devintest.Text("Hi"), devintest.End("resource_exhausted", "slow down")}, llm.CodeRateLimit, llm.ScopeModel, false},
		{"truncated", [][]byte{devintest.Text("Hi")}, llm.CodeUpstream, llm.ScopeEndpoint, false},
	} {
		_, err := relayFrames(c.frames...)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != c.code || e.Scope != c.scope || e.Retryable != c.retryable {
			t.Errorf("%s: err %v (scope %v, retryable %v), want %s on the %s, retryable %v", c.name, err, e != nil && e.Scope != "", e != nil && e.Retryable, c.code, c.scope, c.retryable)
		}
	}
	rec, err := relayFrames(devintest.Text("Hi"), []byte{2, 0, 0, 0, 3, 'b', 'a', 'd'})
	if err != nil || rec.events[len(rec.events)-1].Type != llm.EventFinish {
		t.Errorf("garbled end frame: err %v, events %s; want a clean finish", err, rec.types())
	}
}

// A 429 holds back the model, unless Devin says the account's quota is used up.
func TestStatusErrors(t *testing.T) {
	e := statusError(http.StatusTooManyRequests, "Too many requests", nil, "20")
	if e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeModel || e.RetryAfterMS != 20000 {
		t.Errorf("rate limit: %+v", e)
	}
	e = statusError(http.StatusTooManyRequests, "Weekly quota exhausted", nil, "")
	if e.Code != llm.CodeQuota || e.Scope != llm.ScopeCredential {
		t.Errorf("quota: %+v", e)
	}
}

// What a user pastes in the browser login is read as today's core reads it.
func TestReadPaste(t *testing.T) {
	for _, c := range []struct{ in, token, code string }{
		{"devin-session-token$abc", "devin-session-token$abc", ""},
		{"'eyJhbGc.x.y'", "devin-session-token$eyJhbGc.x.y", ""},
		{"http://127.0.0.1:5000/callback?code=c1&state=s", "", "c1"},
		{"code=c2&state=s", "", "c2"},
		{"  c3  ", "", "c3"},
		{"not a code", "", ""},
	} {
		p := readPaste(c.in)
		if p.token != c.token || p.params.Get("code") != c.code {
			t.Errorf("readPaste(%q) = token %q code %q, want %q %q", c.in, p.token, p.params.Get("code"), c.token, c.code)
		}
	}
}

// Requests carry Devin's CLI headers: the token twice as Basic credentials, Connect's content type
// and version, and no User-Agent.
func TestChatHeaders(t *testing.T) {
	srv := devintest.New(t)
	tok := srv.Token()
	out := &recorder{}
	err := chat(context.Background(), sdk.Call{Account: protocol.Account{Settings: srv.Settings(), Credential: tokenCredential(tok)},
		Request: llm.Request{Model: "swe-2", Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}}}}}, out)
	if err != nil {
		t.Fatal(err)
	}
	h := srv.Requests()[0].Header
	if h.Get("Authorization") != "Basic "+tok+"-"+tok || h.Get("Content-Type") != "application/connect+proto" || h.Get("Connect-Protocol-Version") != "1" {
		t.Errorf("headers %v", h)
	}
	if ua, ok := h["User-Agent"]; ok {
		t.Errorf("User-Agent %q sent, want none", ua)
	}
	if h.Get("Accept-Encoding") != "" {
		t.Errorf("Accept-Encoding %q sent, want none", h.Get("Accept-Encoding"))
	}
}

// The goroutine that forwards login input ends once the login's context is cancelled, even with
// input it cannot hand over: nothing reads the inputs after the login ends.
func TestForwardInputsStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input := func(ctx context.Context) (map[string]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return map[string]string{"code": "x"}, nil
	}
	forwardInputs(ctx, input)
	// Let the forwarder fill the inputs and block handing over the next one.
	time.Sleep(50 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		if !strings.Contains(string(buf[:runtime.Stack(buf, true)]), "forwardInputs.func1") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the input forwarder still runs after the login was cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
