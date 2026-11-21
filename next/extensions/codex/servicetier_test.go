package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A client's service tier reaches Codex whatever its format, through the model's service tier, when
// the routed model's catalog entry lists it (TestServiceTierPerModel); no entry lists ultrafast, so
// a translated request loses it. The subscription's routing hint names the tier that was sent. A
// tier the model does not list is reported as dropped, at the client's field, except auto and
// default, which are Codex's standard service.
func TestServiceTierTranslated(t *testing.T) {
	chat := func(extra string) llm.Request {
		r, err := openaichat.ParseRequest([]byte(`{"model":"gpt-6.1-sol","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return overProtocol(t, r)
	}
	messages := func(extra string) llm.Request {
		r, err := anthropicmessages.ParseRequest([]byte(`{"model":"gpt-6.1-sol","max_tokens":5,"messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return overProtocol(t, r)
	}
	gemini := func(extra string) llm.Request {
		r, err := geminigenerate.ParseRequest("gpt-6.1-sol", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]`+extra+`}`))
		if err != nil {
			t.Fatal(err)
		}
		return overProtocol(t, r)
	}
	responses := func(extra string) llm.Request {
		r, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-6.1-sol","input":"hi"` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return overProtocol(t, r)
	}
	for _, c := range []struct {
		name    string
		req     llm.Request
		tier    string // service_tier of the body Codex gets; "" for none
		dropped []string
	}{
		{"chat priority", chat(`,"service_tier":"priority"`), "priority", nil},
		{"chat fast", chat(`,"service_tier":"fast"`), "priority", nil},
		// No model in the catalog lists ultrafast, so it is dropped and reported.
		{"chat ultrafast", chat(`,"service_tier":"ultrafast"`), "", []string{"service_tier"}},
		{"messages speed fast", messages(`,"speed":"fast"`), "priority", nil},
		{"gemini priority", gemini(`,"serviceTier":"priority"`), "priority", nil},
		{"chat auto", chat(`,"service_tier":"auto"`), "", nil},
		{"chat default", chat(`,"service_tier":"default"`), "", nil},
		{"messages standard_only", messages(`,"service_tier":"standard_only"`), "", nil},
		{"messages auto", messages(`,"service_tier":"auto"`), "", nil},
		{"gemini standard", gemini(`,"serviceTier":"standard"`), "", nil},
		{"chat flex", chat(`,"service_tier":"flex"`), "", []string{"service_tier"}},
		{"chat scale", chat(`,"service_tier":"scale"`), "", []string{"service_tier"}},
		{"gemini flex", gemini(`,"serviceTier":"flex"`), "", []string{"serviceTier"}},
		{"chat none", chat(``), "", nil},
		// A Responses body goes as the client wrote it, and Codex's filter loses what it lost before,
		// without a report.
		{"responses priority", responses(`,"service_tier":"priority"`), "priority", nil},
		{"responses flex", responses(`,"service_tier":"flex"`), "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			ur, err := buildRequest(c.req, "acct", settings{}, account{token: "sk-test", subscription: true, accountID: "chatgpt"}, false)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				ServiceTier string `json:"service_tier"`
			}
			if err := json.Unmarshal(ur.body, &body); err != nil {
				t.Fatal(err)
			}
			if body.ServiceTier != c.tier {
				t.Errorf("upstream service_tier = %q, want %q: %s", body.ServiceTier, c.tier, ur.body)
			}
			if d := ur.tr.DroppedPaths(); !slices.Equal(d, c.dropped) {
				t.Errorf("dropped %q, want %q", d, c.dropped)
			}
			h := http.Header{}
			setClientHeaders(h, c.req, ur, settings{}, account{subscription: true})
			want := "model=gpt-6.1-sol"
			if c.tier != "" {
				want += ";tier=" + c.tier
			}
			if got := h.Get("X-Codex-Routing-Hint"); got != want {
				t.Errorf("routing hint = %q, want %q", got, want)
			}
			if len(c.dropped) > 0 {
				rej := c.req
				rej.DropPolicy = llm.DropReject
				_, err := buildRequest(rej, "acct", settings{}, account{token: "sk-test"}, false)
				var le *llm.Error
				if !errors.As(err, &le) || le.Code != llm.CodeUnsupported || !slices.Equal(le.Dropped, c.dropped) {
					t.Errorf("reject policy: %v, want unsupported naming %q", err, c.dropped)
				}
			}
		})
	}
}

// Verbosity from a Chat client goes to Codex as text.verbosity.
func TestVerbosityTranslated(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"gpt-6.1-sol","verbosity":"low","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ur, err := buildRequest(overProtocol(t, req), "acct", settings{}, account{token: "sk-test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Text struct {
			Verbosity string `json:"verbosity"`
		} `json:"text"`
	}
	if err := json.Unmarshal(ur.body, &body); err != nil || body.Text.Verbosity != "low" {
		t.Errorf("text.verbosity = %q (%v), want low: %s", body.Text.Verbosity, err, ur.body)
	}
	if d := ur.tr.DroppedPaths(); len(d) != 0 {
		t.Errorf("dropped %q, want none", d)
	}
}

// What Codex takes depends on the account's base_url: only OpenAI's API and the ChatGPT backend, the
// two default hosts, are known to take a service tier and a verbosity. At any other host a request
// translated from another format carries neither, and each is reported as dropped at the client's
// field; a Responses client's own body goes as it came, through Codex's filter.
func TestServiceTierByBaseURL(t *testing.T) {
	parsedChat, err := openaichat.ParseRequest([]byte(`{"model":"gpt-6.1-sol","service_tier":"priority","verbosity":"low","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	parsedOwn, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-6.1-sol","service_tier":"priority","text":{"verbosity":"low"},"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	chat, own := overProtocol(t, parsedChat), overProtocol(t, parsedOwn)
	for _, c := range []struct {
		name    string
		req     llm.Request
		set     settings
		sub     bool
		tier    string
		verbose string
		dropped []string
	}{
		{"api default", chat, settings{}, false, "priority", "low", nil},
		{"subscription default", chat, settings{}, true, "priority", "low", nil},
		{"api explicit", chat, settings{BaseURL: "https://API.openai.com/v1/"}, false, "priority", "low", nil},
		{"chatgpt explicit", chat, settings{BaseURL: "https://chatgpt.com/backend-api/codex"}, true, "priority", "low", nil},
		{"custom host", chat, settings{BaseURL: "https://llm.example.com/v1"}, false, "", "", []string{"service_tier", "verbosity"}},
		{"custom host, subscription", chat, settings{BaseURL: "https://llm.example.com/v1"}, true, "", "", []string{"service_tier", "verbosity"}},
		{"lookalike host", chat, settings{BaseURL: "https://api.openai.com.evil.test/v1"}, false, "", "", []string{"service_tier", "verbosity"}},
		{"own body, custom host", own, settings{BaseURL: "https://llm.example.com/v1"}, false, "priority", "low", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			ur, err := buildRequest(c.req, "acct", c.set, account{token: "sk-test", subscription: c.sub}, false)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				ServiceTier string `json:"service_tier"`
				Text        struct {
					Verbosity string `json:"verbosity"`
				} `json:"text"`
			}
			if err := json.Unmarshal(ur.body, &body); err != nil {
				t.Fatal(err)
			}
			if body.ServiceTier != c.tier || body.Text.Verbosity != c.verbose {
				t.Errorf("body has service_tier %q and verbosity %q, want %q and %q: %s", body.ServiceTier, body.Text.Verbosity, c.tier, c.verbose, ur.body)
			}
			if d := ur.tr.DroppedPaths(); !slices.Equal(d, c.dropped) {
				t.Errorf("dropped %q, want %q", d, c.dropped)
			}
		})
	}
}

// The routed model's entry in the client catalog says what a translated request may carry, as the
// Codex CLI filters on the same data: a service tier only if the entry lists it ("fast" counts as
// "priority"), a verbosity only if it takes one. A model the catalog does not list takes neither.
// Each is reported as dropped at the client's field, except auto and default, which ask for the
// standard service every request gets and are left off with no loss. A Responses client's own body
// is not narrowed.
func TestServiceTierPerModel(t *testing.T) {
	for _, c := range []struct {
		name, model, extra string
		tier, verbosity    string
		dropped            []string
	}{
		{"listed tier and verbosity", "gpt-6.1-sol", `,"service_tier":"priority","verbosity":"low"`, "priority", "low", nil},
		{"fast is priority", "gpt-6.1-sol", `,"service_tier":"fast"`, "priority", "", nil},
		{"ultrafast, no model lists it", "gpt-6.1-sol", `,"service_tier":"ultrafast"`, "", "", []string{"service_tier"}},
		{"flex", "gpt-6.1-sol", `,"service_tier":"flex"`, "", "", []string{"service_tier"}},
		{"auto is standard service", "gpt-6.1-sol", `,"service_tier":"auto"`, "", "", nil},
		{"default is standard service", "gpt-6.1-sol", `,"service_tier":"default"`, "", "", nil},
		{"model with no tiers and no verbosity", "gpt-6-chat-thinking", `,"service_tier":"priority","verbosity":"low"`, "", "", []string{"service_tier", "verbosity"}},
		{"model with no tiers, auto", "gpt-6-chat-thinking", `,"service_tier":"auto"`, "", "", nil},
		{"model not in the catalog", "gpt-not-listed", `,"service_tier":"priority","verbosity":"low"`, "", "", []string{"service_tier", "verbosity"}},
		{"a verbosity OpenAI does not list", "gpt-6.1-sol", `,"verbosity":"loud"`, "", "", []string{"verbosity"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := openaichat.ParseRequest([]byte(`{"model":"` + c.model + `","messages":[{"role":"user","content":"hi"}]` + c.extra + `}`))
			if err != nil {
				t.Fatal(err)
			}
			req := overProtocol(t, parsed)
			ur, err := buildRequest(req, "acct", settings{}, account{token: "sk-test"}, false)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				ServiceTier string `json:"service_tier"`
				Text        struct {
					Verbosity string `json:"verbosity"`
				} `json:"text"`
			}
			if err := json.Unmarshal(ur.body, &body); err != nil {
				t.Fatal(err)
			}
			if body.ServiceTier != c.tier || body.Text.Verbosity != c.verbosity {
				t.Errorf("body has service_tier %q and verbosity %q, want %q and %q: %s", body.ServiceTier, body.Text.Verbosity, c.tier, c.verbosity, ur.body)
			}
			if d := ur.tr.DroppedPaths(); !slices.Equal(d, c.dropped) {
				t.Errorf("dropped %q, want %q", d, c.dropped)
			}
			if len(c.dropped) > 0 {
				rej := req
				rej.DropPolicy = llm.DropReject
				_, err := buildRequest(rej, "acct", settings{}, account{token: "sk-test"}, false)
				var le *llm.Error
				if !errors.As(err, &le) || le.Code != llm.CodeUnsupported || !slices.Equal(le.Dropped, c.dropped) {
					t.Errorf("reject policy: %v, want unsupported naming %q", err, c.dropped)
				}
			}
		})
	}
	// A Responses client's own body is not narrowed to the model.
	own, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-6-chat-thinking","service_tier":"priority","text":{"verbosity":"low"},"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	ur, err := buildRequest(own, "acct", settings{}, account{token: "sk-test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ur.body), `"service_tier":"priority"`) || !strings.Contains(string(ur.body), `"verbosity":"low"`) || len(ur.tr.Losses) != 0 {
		t.Errorf("a Responses client's own body was narrowed: %s, %+v", ur.body, ur.tr.Losses)
	}
}

// overProtocol returns r as the extension gets it: encoded to JSON and decoded again, as the
// extension protocol carries a request from core, so a test of a body builder starts from a client's
// real body parsed by its format's own parser and sent the way core sends it.
func overProtocol(t *testing.T, r llm.Request) llm.Request {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out llm.Request
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
