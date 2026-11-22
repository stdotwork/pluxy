package main

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// officialBase is the base URL of the vendor whose documentation names a service tier and a
// verbosity on each wire (tierHosts); nothing is sent to it.
var officialBase = map[string]string{
	llm.FormatOpenAIChat:        "https://api.openai.com/v1",
	llm.FormatOpenAIResponses:   "https://api.openai.com/v1",
	llm.FormatAnthropicMessages: "https://api.anthropic.com/v1",
	llm.FormatGeminiGenerate:    "https://generativelanguage.googleapis.com/v1beta",
}

var wireOf = map[string]string{
	"chat": llm.FormatOpenAIChat, "responses": llm.FormatOpenAIResponses,
	"messages": llm.FormatAnthropicMessages, "gemini": llm.FormatGeminiGenerate,
}

// tierClient reads a client request in a format.
func tierClient(t *testing.T, kind, body string) llm.Request {
	t.Helper()
	var req llm.Request
	var err error
	switch kind {
	case "chat":
		req, err = openaichat.ParseRequest([]byte(body))
	case "responses":
		req, err = openairesponses.ParseRequest([]byte(body))
	case "messages":
		req, err = anthropicmessages.ParseRequest([]byte(body))
	case "gemini":
		req, err = geminigenerate.ParseRequest("m", false, []byte(body))
	}
	if err != nil {
		t.Fatal(err)
	}
	// As the extension gets it: over the protocol, encoded to JSON and decoded again.
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var out llm.Request
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// tierClients are requests that name a service tier or a verbosity, by the format they are in.
var tierClients = map[string]struct{ kind, body string }{
	"chat-priority":      {"chat", `{"model":"m","service_tier":"priority","verbosity":"low","messages":[{"role":"user","content":"Hi"}]}`},
	"chat-fast":          {"chat", `{"model":"m","service_tier":"fast","messages":[{"role":"user","content":"Hi"}]}`},
	"chat-flex":          {"chat", `{"model":"m","service_tier":"flex","messages":[{"role":"user","content":"Hi"}]}`},
	"chat-auto":          {"chat", `{"model":"m","service_tier":"auto","messages":[{"role":"user","content":"Hi"}]}`},
	"responses-priority": {"responses", `{"model":"m","service_tier":"priority","text":{"verbosity":"high"},"input":"Hi"}`},
	"messages-fast":      {"messages", `{"model":"m","max_tokens":9,"speed":"fast","messages":[{"role":"user","content":"Hi"}]}`},
	"messages-auto":      {"messages", `{"model":"m","max_tokens":9,"service_tier":"auto","messages":[{"role":"user","content":"Hi"}]}`},
	"gemini-priority":    {"gemini", `{"serviceTier":"priority","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`},
	"gemini-standard":    {"gemini", `{"serviceTier":"standard","contents":[{"role":"user","parts":[{"text":"Hi"}]}]}`},
}

// tierWant is, per client and wire, the fields of the upstream body that say a tier or a verbosity,
// at the vendor whose documentation names them (officialBase): "service_tier", "verbosity",
// "text.verbosity", "speed" or "serviceTier" with the value.
var tierWant = map[string]map[string]map[string]string{
	"chat-priority":      {"chat": {"service_tier": "priority", "verbosity": "low"}, "responses": {"service_tier": "priority", "text.verbosity": "low"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
	"chat-fast":          {"chat": {"service_tier": "fast"}, "responses": {"service_tier": "fast"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
	"chat-flex":          {"chat": {"service_tier": "flex"}, "responses": {"service_tier": "flex"}, "messages": {}, "gemini": {"serviceTier": "flex"}},
	"chat-auto":          {"chat": {"service_tier": "auto"}, "responses": {"service_tier": "auto"}, "messages": {"service_tier": "auto"}, "gemini": {}},
	"responses-priority": {"chat": {"service_tier": "priority", "verbosity": "high"}, "responses": {"service_tier": "priority", "text.verbosity": "high"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
	"messages-fast":      {"chat": {"service_tier": "priority"}, "responses": {"service_tier": "priority"}, "messages": {"speed": "fast"}, "gemini": {"serviceTier": "priority"}},
	"messages-auto":      {"chat": {"service_tier": "auto"}, "responses": {"service_tier": "auto"}, "messages": {"service_tier": "auto"}, "gemini": {}},
	"gemini-priority":    {"chat": {"service_tier": "priority"}, "responses": {"service_tier": "priority"}, "messages": {}, "gemini": {"serviceTier": "priority"}},
	"gemini-standard":    {"chat": {"service_tier": "default"}, "responses": {"service_tier": "default"}, "messages": {"service_tier": "standard_only"}, "gemini": {"serviceTier": "standard"}},
}

// tierFieldsIn returns the fields of body that say a tier or a verbosity.
func tierFieldsIn(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range []string{"service_tier", "verbosity", "speed", "serviceTier"} {
		if v, ok := sent[f]; ok {
			var s string
			_ = json.Unmarshal(v, &s)
			got[f] = s
		}
	}
	var text struct {
		Verbosity string `json:"verbosity"`
	}
	_ = json.Unmarshal(sent["text"], &text)
	if text.Verbosity != "" {
		got["text.verbosity"] = text.Verbosity
	}
	return got
}

// A client's service tier and verbosity reach the vendor whose documentation names them, in the form
// its wire has for them, and what the wire cannot say is dropped, not moved to another setting.
// (The same requests go to a third-party host without either: see TestServiceTierThirdPartyHost.)
func TestServiceTierToDocumentedHosts(t *testing.T) {
	for name, c := range tierClients {
		for wire, format := range wireOf {
			t.Run(name+" to "+wire, func(t *testing.T) {
				req := tierClient(t, c.kind, c.body)
				body, _, err := upstreamBody(req, format, settings{BaseURL: officialBase[format]})
				if err != nil {
					t.Fatal(err)
				}
				if got := tierFieldsIn(t, body); !maps.Equal(got, tierWant[name][wire]) {
					t.Errorf("upstream body says %v, want %v: %s", got, tierWant[name][wire], body)
				}
			})
		}
	}
}

// A third-party host (or any host not listed in tierHosts) gets neither a tier nor a verbosity from
// a request in another format, and the report says so, at the client's field. A client's own body
// goes as it came.
func TestServiceTierToThirdPartyHost(t *testing.T) {
	for name, c := range tierClients {
		for wire, format := range wireOf {
			t.Run(name+" to "+wire, func(t *testing.T) {
				req := tierClient(t, c.kind, c.body)
				body, tr, err := upstreamBody(req, format, settings{BaseURL: "https://llm.example.com/v1"})
				if err != nil {
					t.Fatal(err)
				}
				got := tierFieldsIn(t, body)
				if c.kind == wire {
					// The client's own body: untouched, and nothing lost.
					if string(body) != c.body || len(tr.Losses) != 0 {
						t.Errorf("a %s client's body changed or lost something: %s, %+v", c.kind, body, tr.Losses)
					}
					return
				}
				if len(got) != 0 {
					t.Errorf("upstream body says %v: %s", got, body)
				}
				want := []string{llm.ServiceTierPath(req)}
				if req.Options.Verbosity != "" {
					want = append(want, llm.VerbosityPath(req))
				}
				if d := tr.DroppedPaths(); !slices.Equal(d, want) {
					t.Errorf("dropped %q, want %q", d, want)
				}
			})
		}
	}
}

// The host table is by host, not by what the base URL's path or case says, and an account whose
// base URL is not one of the vendors' gets none.
func TestTierFieldsOf(t *testing.T) {
	for _, c := range []struct {
		base, format string
		want         tierFields
	}{
		{"https://api.openai.com/v1", llm.FormatOpenAIChat, tierFields{serviceTier: true, verbosity: true, webSearch: true, candidateCount: true}},
		{"https://API.OpenAI.com/v1/", llm.FormatOpenAIResponses, tierFields{serviceTier: true, verbosity: true, webSearch: true, codeExecution: true}},
		{"https://api.openai.com/v1", llm.FormatAnthropicMessages, tierFields{}},
		{"https://api.anthropic.com", llm.FormatAnthropicMessages, tierFields{serviceTier: true, webSearch: true, codeExecution: true}},
		{"https://api.anthropic.com", llm.FormatOpenAIChat, tierFields{}},
		{"https://generativelanguage.googleapis.com/v1beta", llm.FormatGeminiGenerate, tierFields{serviceTier: true, webSearch: true, codeExecution: true, candidateCount: true}},
		{"https://api.mistral.ai/v1", llm.FormatOpenAIChat, tierFields{}},
		{"https://api.openai.com.evil.test/v1", llm.FormatOpenAIChat, tierFields{}},
		{"http://127.0.0.1:1234/v1", llm.FormatOpenAIChat, tierFields{}},
		{"", llm.FormatOpenAIChat, tierFields{}},
		{"://bad", llm.FormatOpenAIChat, tierFields{}},
	} {
		if got := tierFieldsOf(settings{BaseURL: c.base}, c.format); got != c.want {
			t.Errorf("tierFieldsOf(%q, %s) = %+v, want %+v", c.base, c.format, got, c.want)
		}
	}
}

// Under the reject drop policy a tier or verbosity the vendor's wire carries goes, and one it
// cannot carry (or any at a third-party host) refuses the request.
func TestServiceTierRejectPolicy(t *testing.T) {
	for _, c := range []struct {
		name, client, base, wire string
		dropped                  []string // the fields the refusal names; none: the request goes
	}{
		{"chat priority to anthropic", "chat-priority", "https://api.anthropic.com/v1", "messages", []string{"service_tier", "verbosity"}},
		{"chat flex to anthropic", "chat-flex", "https://api.anthropic.com/v1", "messages", []string{"service_tier"}},
		{"chat auto to gemini", "chat-auto", "https://generativelanguage.googleapis.com/v1beta", "gemini", []string{"service_tier"}},
		{"messages fast to openai", "messages-fast", "https://api.openai.com/v1", "chat", nil},
		{"gemini priority to openai responses", "gemini-priority", "https://api.openai.com/v1", "responses", nil},
		{"chat auto to anthropic", "chat-auto", "https://api.anthropic.com/v1", "messages", nil},
		{"messages fast to third party", "messages-fast", "https://llm.example.com/v1", "chat", []string{"speed"}},
		{"gemini priority to third party", "gemini-priority", "https://llm.example.com/v1", "responses", []string{"serviceTier"}},
		{"chat auto to third party", "chat-auto", "https://llm.example.com/v1", "messages", []string{"service_tier"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl := tierClients[c.client]
			req := tierClient(t, cl.kind, cl.body)
			req.DropPolicy = llm.DropReject
			_, _, err := upstreamBody(req, wireOf[c.wire], settings{BaseURL: c.base})
			if c.dropped == nil {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			var le *llm.Error
			if !errors.As(err, &le) || le.Code != llm.CodeUnsupported || !slices.Equal(le.Dropped, c.dropped) {
				t.Errorf("error %v, want unsupported naming %q", err, c.dropped)
			}
		})
	}
}
