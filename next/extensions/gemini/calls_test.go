package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A Gemini client's own body gets the repair a translated one gets: function responses go out in
// the order of the calls they answer, matched by ID, then by name, then by position, and one with
// no name takes its call's name.
func TestFixTurnsOrdersNativeFunctionResponses(t *testing.T) {
	const (
		user  = `{"role":"user","parts":[{"text":"Weather and time?"}]}`
		calls = `{"role":"model","parts":[{"functionCall":{"id":"fc-a","name":"get_weather","args":{"city":"Oslo"}},"thoughtSignature":"sig"},` +
			`{"functionCall":{"id":"fc-b","name":"get_time","args":{}}}]}`
		noIDs = `{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Oslo"}},"thoughtSignature":"sig"},` +
			`{"functionCall":{"name":"get_time","args":{}}}]}`
	)
	for _, c := range []struct {
		name, model, responses, want string
	}{
		{"by ID", calls, `[{"functionResponse":{"id":"fc-b","name":"get_time","response":{"t":"12:00"}}},{"functionResponse":{"id":"fc-a","name":"get_weather","response":{"c":4}}}]`,
			`[{"functionResponse":{"id":"fc-a","name":"get_weather","response":{"c":4}}},{"functionResponse":{"id":"fc-b","name":"get_time","response":{"t":"12:00"}}}]`},
		{"by name", noIDs, `[{"functionResponse":{"name":"get_time","response":{"t":"12:00"}}},{"functionResponse":{"name":"get_weather","response":{"c":4}}}]`,
			`[{"functionResponse":{"name":"get_weather","response":{"c":4}}},{"functionResponse":{"name":"get_time","response":{"t":"12:00"}}}]`},
		{"no name takes its call's", calls, `[{"functionResponse":{"name":"","response":{"c":4}}},{"functionResponse":{"id":"fc-b","name":"get_time","response":{"t":"12:00"}}}]`,
			`[{"functionResponse":{"name":"get_weather","response":{"c":4}}},{"functionResponse":{"id":"fc-b","name":"get_time","response":{"t":"12:00"}}}]`},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := []byte(`{"contents":[` + user + "," + c.model + `,{"role":"user","parts":` + c.responses + `}]}`)
			var got struct {
				Contents []struct {
					Parts json.RawMessage `json:"parts"`
				} `json:"contents"`
			}
			out := fixTurns(body, false, false, false)
			if err := json.Unmarshal(out, &got); err != nil || len(got.Contents) != 3 {
				t.Fatalf("fixTurns gave %s (%v)", out, err)
			}
			if !jsonEqual(got.Contents[2].Parts, c.want) {
				t.Errorf("responses %s, want %s", got.Contents[2].Parts, c.want)
			}
		})
	}
	// Responses already in order are left byte for byte.
	body := []byte(`{"contents":[` + user + "," + calls + `,{"role":"user","parts":[{"functionResponse":{"id":"fc-a","name":"get_weather","response":{"c":4}}},{"functionResponse":{"id":"fc-b","name":"get_time","response":{"t":"12:00"}}}]}]}`)
	if out := fixTurns(body, false, false, false); string(out) != string(body) {
		t.Errorf("an ordered body changed: %s", out)
	}
}

func jsonEqual(a json.RawMessage, b string) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal([]byte(b), &y) == nil && fmt.Sprint(x) == fmt.Sprint(y)
}

// recorder takes a reply's events, as core's stream would.
type recorder struct{ events []llm.Event }

func (r *recorder) Send(ev llm.Event) error { r.events = append(r.events, ev); return nil }

func (r *recorder) text() string {
	var b strings.Builder
	for _, ev := range r.events {
		if ev.Type == llm.EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
	return b.String()
}

func (r *recorder) count(typ string) int {
	n := 0
	for _, ev := range r.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// malformedReply writes a reply that ends with MALFORMED_FUNCTION_CALL, after a thought (which a
// stream sends first) and, with text, some answer text.
func malformedReply(w http.ResponseWriter, stream bool, text string) {
	thought := `{"text":"I should call get_weather.","thought":true}`
	finish := `{"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"MALFORMED_FUNCTION_CALL",` +
		`"finishMessage":"Malformed function call: print(default_api.get_weather(city=))","index":0}],` +
		`"usageMetadata":{"promptTokenCount":11,"totalTokenCount":11},"responseId":"bad"}`
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.Replace(finish, `{"text":""}`, thought, 1))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[`+thought+`]},"index":0}],"responseId":"bad"}`+"\n\n")
	if text != "" {
		t, _ := json.Marshal(text)
		io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":`+string(t)+`}]},"index":0}],"responseId":"bad"}`+"\n\n")
	}
	io.WriteString(w, "data: "+finish+"\n\n")
}

// A reply that ends with MALFORMED_FUNCTION_CALL is asked for again once, through the
// extension's real request path against the fake Google. The client sees only the retry's reply;
// if the retry fails the same way, it gets an error that says so. A stream that has already sent
// answer text is not retried (the client has it), and ends with the error.
func TestMalformedFunctionCallRetriedOnce(t *testing.T) {
	native := &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(`{"contents":[{"role":"user","parts":[{"text":"Weather in Oslo?"}]}]}`)}
	chat := llm.Request{Native: &llm.Native{Format: llm.FormatOpenAIChat, Body: json.RawMessage(`{}`)},
		Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Weather in Oslo?"}}}}}
	for _, c := range []struct {
		name      string
		stream    bool
		chat      bool
		malformed int    // how many replies end malformed before a good one
		text      string // answer text the malformed stream sends first
		requests  int
		wantText  string
		wantError bool
	}{
		{name: "non-stream, retry succeeds", malformed: 1, requests: 2, wantText: "Hello"},
		{name: "stream, retry succeeds", stream: true, malformed: 1, requests: 2, wantText: "Hello"},
		{name: "chat stream, retry succeeds", stream: true, chat: true, malformed: 1, requests: 2, wantText: "Hello"},
		{name: "non-stream, retry fails", malformed: 2, requests: 2, wantError: true},
		{name: "stream, retry fails", stream: true, malformed: 2, requests: 2, wantError: true},
		{name: "chat stream, retry fails", stream: true, chat: true, malformed: 2, requests: 2, wantError: true},
		{name: "stream after answer text", stream: true, malformed: 1, text: "Let me check. ", requests: 1, wantText: "Let me check. ", wantError: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := geminitest.New(t)
			n := 0
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				n++
				if n <= c.malformed {
					malformedReply(w, req.Stream, c.text)
					return
				}
				if req.Stream {
					geminitest.StreamText(w, "Hel", "lo")
					return
				}
				geminitest.JSONText(w, "Hello")
			}
			req := llm.Request{Model: "gemini-x", Stream: c.stream, Native: native}
			if c.chat {
				req = chat
				req.Model, req.Stream = "gemini-x", c.stream
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "g", Settings: settingsFor(srv, kindGemini),
					Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}},
				Request: req,
			}
			rec := &recorder{}
			err := generateTo(context.Background(), call, rec)
			if got := len(srv.Requests()); got != c.requests {
				t.Errorf("%d upstream requests, want %d", got, c.requests)
			}
			if got := rec.text(); got != c.wantText {
				t.Errorf("client text %q, want %q", got, c.wantText)
			}
			if n := rec.count(llm.EventResponseStart); n > 1 {
				t.Errorf("%d response starts, want at most 1", n)
			}
			if n := rec.count(llm.EventReasoningDelta); c.wantText == "Hello" && n != 0 {
				t.Errorf("the client got %d reasoning deltas of the reply that was asked for again", n)
			}
			if !c.wantError {
				if err != nil || rec.count(llm.EventFinish) != 1 {
					t.Errorf("err = %v, finish events %d; want the retry's reply, finished", err, rec.count(llm.EventFinish))
				}
				return
			}
			var e *llm.Error
			if !errors.As(err, &e) || e.ProviderCode != "MALFORMED_FUNCTION_CALL" || !strings.Contains(e.Message, "MALFORMED_FUNCTION_CALL") {
				t.Fatalf("err = %v, want an error naming MALFORMED_FUNCTION_CALL", err)
			}
			if rec.count(llm.EventFinish) != 0 {
				t.Error("the client got a finish as well as the error")
			}
		})
	}
}
