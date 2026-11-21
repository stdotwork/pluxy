package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

var (
	// apiSettings is an account at the Gemini API, vertexSettingsTest one on Vertex AI and customHost
	// one whose base_url is a third-party host.
	apiSettings        = settings{BaseURL: defaultBaseURL}
	vertexSettingsTest = settings{Vertex: &vertexSettings{}}
	customHost         = settings{BaseURL: "https://llm.example.com"}
)

// A client's service tier reaches the Gemini API as its serviceTier: default is standard, flex and
// priority are themselves (a Chat or Responses client's fast is priority, a Messages client's fast
// speed too). Auto, scale, ultrafast and a tier the model does not know have none: they are
// reported as dropped at the client's field, never moved to another tier, and refuse the request
// under the reject drop policy. Verbosity has no place in a GenerateContent request. Vertex AI has
// no service tier at all.
func TestServiceTierTranslated(t *testing.T) {
	chat := func(extra string) llm.Request {
		r, err := openaichat.ParseRequest([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	responses := func(extra string) llm.Request {
		r, err := openairesponses.ParseRequest([]byte(`{"model":"gemini-2.5-pro","input":"hi"` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	messages := func(extra string) llm.Request {
		r, err := anthropicmessages.ParseRequest([]byte(`{"model":"gemini-2.5-pro","messages":[{"role":"user","content":"hi"}]` + extra + `}`))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, c := range []struct {
		name    string
		req     llm.Request
		tier    string // serviceTier of the body; "" for none
		dropped []string
	}{
		{"chat default", chat(`,"service_tier":"default"`), "standard", nil},
		{"chat flex", chat(`,"service_tier":"flex"`), "flex", nil},
		{"chat priority", chat(`,"service_tier":"priority"`), "priority", nil},
		{"chat fast", chat(`,"service_tier":"fast"`), "priority", nil},
		{"responses priority", responses(`,"service_tier":"priority"`), "priority", nil},
		{"messages speed fast", messages(`,"speed":"fast"`), "priority", nil},
		{"messages standard_only", messages(`,"service_tier":"standard_only"`), "standard", nil},
		{"chat auto", chat(`,"service_tier":"auto"`), "", []string{"service_tier"}},
		{"messages auto", messages(`,"service_tier":"auto"`), "", []string{"service_tier"}},
		{"chat scale", chat(`,"service_tier":"scale"`), "", []string{"service_tier"}},
		{"chat ultrafast", chat(`,"service_tier":"ultrafast"`), "", []string{"service_tier"}},
		{"chat unknown", chat(`,"service_tier":"turbo"`), "", []string{"service_tier"}},
		{"chat none", chat(``), "", nil},
		{"chat verbosity", chat(`,"verbosity":"low"`), "", []string{"verbosity"}},
		{"responses verbosity", responses(`,"text":{"verbosity":"low"}`), "", []string{"text.verbosity"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, tr, err := buildBodyOn(c.req, apiSettings)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				ServiceTier string `json:"serviceTier"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.ServiceTier != c.tier {
				t.Errorf("serviceTier = %q, want %q: %s", got.ServiceTier, c.tier, body)
			}
			if d := tr.DroppedPaths(); !slices.Equal(d, c.dropped) {
				t.Errorf("dropped %q, want %q", d, c.dropped)
			}
			if len(c.dropped) > 0 {
				rej := c.req
				rej.DropPolicy = llm.DropReject
				_, _, err := buildBodyOn(rej, apiSettings)
				var le *llm.Error
				if !errors.As(err, &le) || le.Code != llm.CodeUnsupported || !slices.Equal(le.Dropped, c.dropped) {
					t.Errorf("reject policy: %v, want unsupported naming %q", err, c.dropped)
				}
			}
			// Vertex AI sends no tier, and reports the one it would have sent.
			vbody, vtr, err := buildBodyOn(c.req, vertexSettingsTest)
			if err != nil {
				t.Fatal(err)
			}
			var vgot struct {
				ServiceTier string `json:"serviceTier"`
			}
			if err := json.Unmarshal(vbody, &vgot); err != nil || vgot.ServiceTier != "" {
				t.Errorf("Vertex serviceTier = %q (%v), want none: %s", vgot.ServiceTier, err, vbody)
			}
			want := c.dropped
			if c.tier != "" {
				want = []string{llm.ServiceTierPath(c.req)}
			}
			if d := vtr.DroppedPaths(); !slices.Equal(d, want) {
				t.Errorf("Vertex dropped %q, want %q", d, want)
			}
		})
	}
}

// A Gemini client's body goes as it wrote it, on the Gemini API and on Vertex AI alike.
func TestServiceTierGeminiPassthrough(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"serviceTier":"flex"}`
	req := llm.Request{Model: "gemini-2.5-pro", Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(body)}}
	for _, st := range []settings{apiSettings, vertexSettingsTest, customHost} {
		got, tr, err := buildBodyOn(req, st)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body || !tr.Passthrough || len(tr.Losses) != 0 {
			t.Errorf("%+v: body %s, report %+v, want the body as sent and no losses", st, got, tr)
		}
	}
}

// An account whose base_url is not the Gemini API's gets no service tier from a translated request,
// as Vertex AI does not: the same loss at the client's field, and the request still goes (and is
// refused under the reject drop policy, as a Vertex one is).
func TestServiceTierCustomHost(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"m","service_tier":"priority","verbosity":"low","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]settings{"custom": customHost, "vertex": vertexSettingsTest} {
		body, tr, err := buildBodyOn(req, st)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "serviceTier") {
			t.Errorf("%s: body has a service tier: %s", name, body)
		}
		if d := tr.DroppedPaths(); !slices.Equal(d, []string{"service_tier", "verbosity"}) {
			t.Errorf("%s: dropped %q, want service_tier and verbosity", name, d)
		}
		rej := req
		rej.DropPolicy = llm.DropReject
		if _, _, err := buildBodyOn(rej, st); err == nil {
			t.Errorf("%s: a tier was not refused under reject", name)
		}
	}
	// The Gemini API's host, in any letter case and with a path or port, takes one.
	for _, base := range []string{"https://generativelanguage.googleapis.com", "https://GenerativeLanguage.googleapis.com/v1beta"} {
		body, _, err := buildBodyOn(req, settings{BaseURL: base})
		if err != nil || !strings.Contains(string(body), `"serviceTier":"priority"`) {
			t.Errorf("%s: %v, body %s, want serviceTier priority", base, err, body)
		}
	}
}
