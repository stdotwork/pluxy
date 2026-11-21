package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// A client's service tier reaches Codex whatever its format, through the model's service tier, and
// Codex's filter then keeps what its endpoint takes: priority (fast too) and ultrafast. The
// subscription's routing hint names the tier that was sent. A tier it cannot take is reported as
// dropped for a request translated from another format, at the client's field.
func TestServiceTierTranslated(t *testing.T) {
	chat := func(extra string) llm.Request {
		r, err := openaichat.ParseRequest([]byte(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	messages := func(extra string) llm.Request {
		r, err := anthropicmessages.ParseRequest([]byte(`{"model":"gpt-test","max_tokens":5,"messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	gemini := func(extra string) llm.Request {
		r, err := geminigenerate.ParseRequest("gpt-test", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]`+extra+`}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	responses := func(extra string) llm.Request {
		r, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-test","input":"hi"` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, c := range []struct {
		name    string
		req     llm.Request
		tier    string // service_tier of the body Codex gets; "" for none
		dropped []string
	}{
		{"chat priority", chat(`,"service_tier":"priority"`), "priority", nil},
		{"chat fast", chat(`,"service_tier":"fast"`), "priority", nil},
		{"chat ultrafast", chat(`,"service_tier":"ultrafast"`), "ultrafast", nil},
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
			want := "model=gpt-test"
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
	req, err := openaichat.ParseRequest([]byte(`{"model":"gpt-test","verbosity":"low","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ur, err := buildRequest(req, "acct", settings{}, account{token: "sk-test"}, false)
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
	chat, err := openaichat.ParseRequest([]byte(`{"model":"gpt-test","service_tier":"priority","verbosity":"low","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	own, err := openairesponses.ParseRequest([]byte(`{"model":"gpt-test","service_tier":"priority","text":{"verbosity":"low"},"input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
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
