package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func quotaOf(t *testing.T, srv *xaitest.Server, c *protocol.Credential) (*protocol.Quota, error) {
	t.Helper()
	return quota(context.Background(), sdk.QuotaCall{Account: protocol.Account{ID: "grok", Settings: srv.Settings(), Credential: c}})
}

func limitByID(t *testing.T, q *protocol.Quota, id string) protocol.QuotaLimit {
	t.Helper()
	for _, l := range q.Limits {
		if l.ID == id {
			return l
		}
	}
	t.Fatalf("no limit %q in %+v", id, q.Limits)
	return protocol.QuotaLimit{}
}

func f(v float64) *float64 { return &v }

func same(a, b *float64) bool { return a != nil && b != nil && *a == *b }

// A Grok login's quota is read with two billing GETs and two profile GETs on the CLI endpoint, each
// with the login's token and the Grok CLI's identity, and nothing else (no generation); the limits
// carry the weekly window, the monthly credits (used clamped to the allowance, the rest counted as
// pay as you go), the pay-as-you-go cap and the prepaid balance.
func TestQuota(t *testing.T) {
	srv := xaitest.New(t)
	access, _ := srv.Grant()
	q, err := quotaOf(t, srv, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access})
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	var got []string
	for _, r := range reqs {
		got = append(got, r.Method+" "+r.Path+"?"+r.Query)
		h := r.Header
		if h.Get("Authorization") != "Bearer "+access || h.Get("User-Agent") != "xai-grok-workspace/"+cliVersion || h.Get("X-XAI-Token-Auth") != "xai-grok-cli" ||
			h.Get("x-grok-client-version") != cliVersion || h.Get("x-grok-client-identifier") != "grok-shell" || h.Get("x-authenticateresponse") != "authenticate-response" {
			t.Errorf("%s %s headers %v", r.Method, r.Path, h)
		}
	}
	want := []string{"GET /v1/billing?format=credits", "GET /v1/billing?", "GET /v1/user?include=subscription", "GET /v1/settings?"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("requests %q, want %q", got, want)
	}
	if q.Plan != "SuperGrok" {
		t.Errorf("plan %q", q.Plan)
	}
	if len(q.Limits) != 4 {
		t.Fatalf("limits %+v", q.Limits)
	}
	w := limitByID(t, q, "weekly")
	if w.Label != "Weekly limit" || !same(w.UsedPercent, f(42.5)) || w.ResetsAt != 1794182400 || w.WindowSeconds != 7*24*3600 {
		t.Errorf("weekly %+v", w)
	}
	m := limitByID(t, q, "monthly_credits")
	if m.Label != "Monthly credits" || m.Unit != "usd" || !same(m.Used, f(150)) || !same(m.Limit, f(150)) || !same(m.Remaining, f(0)) || !same(m.UsedPercent, f(100)) ||
		m.ResetsAt != 1796083200 || m.WindowSeconds != 30*24*3600 {
		t.Errorf("monthly %+v", m)
	}
	o := limitByID(t, q, "pay_as_you_go")
	if o.Label != "Pay as you go" || o.Unit != "usd" || !same(o.Used, f(10)) || !same(o.Limit, f(50)) || !same(o.Remaining, f(40)) || !same(o.UsedPercent, f(20)) {
		t.Errorf("pay as you go %+v", o)
	}
	p := limitByID(t, q, "prepaid")
	if p.Label != "Prepaid" || p.Unit != "usd" || !same(p.Remaining, f(25)) {
		t.Errorf("prepaid %+v", p)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(q.Raw, &raw); err != nil || len(raw) != 4 {
		t.Errorf("raw %s: %v", q.Raw, err)
	}
	if strings.Contains(string(q.Raw), access) {
		t.Error("raw holds the token")
	}
}

// An API key has no plan to report.
func TestQuotaAPIKeyUnsupported(t *testing.T) {
	srv := xaitest.New(t)
	_, err := quotaOf(t, srv, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.Key()})
	var e *llm.Error
	if !asLLM(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("error %v, want unsupported with scope credential", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("%d requests reached the fake for an API key", n)
	}
}

func asLLM(err error, e **llm.Error) bool {
	x, ok := err.(*llm.Error)
	*e = x
	return ok
}

// A refused token is an auth error with scope credential; a rate limit and an outage keep their
// codes; a reply with no usage in it is unsupported, not an error.
func TestQuotaErrors(t *testing.T) {
	cred := func(srv *xaitest.Server) *protocol.Credential {
		a, _ := srv.Grant()
		return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: a}
	}
	t.Run("401", func(t *testing.T) {
		srv := xaitest.New(t)
		a, _ := srv.Grant()
		srv.Revoke(a)
		_, err := quotaOf(t, srv, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: a})
		var e *llm.Error
		if !asLLM(err, &e) || e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || e.Status != http.StatusUnauthorized {
			t.Errorf("error %#v", err)
		}
	})
	t.Run("429", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Billing = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"slow down"}`, http.StatusTooManyRequests)
		}
		_, err := quotaOf(t, srv, cred(srv))
		var e *llm.Error
		if !asLLM(err, &e) || e.Code != llm.CodeRateLimit || e.Scope != llm.ScopeCredential {
			t.Errorf("error %#v", err)
		}
	})
	t.Run("500", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Billing = func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
		}
		_, err := quotaOf(t, srv, cred(srv))
		var e *llm.Error
		if !asLLM(err, &e) || e.Code != llm.CodeUpstream {
			t.Errorf("error %#v", err)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := xaitest.New(t)
		c := cred(srv)
		settings := srv.Settings()
		srv.Close()
		_, err := quota(context.Background(), sdk.QuotaCall{Account: protocol.Account{Settings: settings, Credential: c}})
		var e *llm.Error
		if !asLLM(err, &e) || e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("error %#v", err)
		}
	})
	t.Run("no usage", func(t *testing.T) {
		srv := xaitest.New(t)
		srv.Billing = func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"config":{}}`)) }
		_, err := quotaOf(t, srv, cred(srv))
		var e *llm.Error
		if !asLLM(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
			t.Errorf("error %#v", err)
		}
	})
}

// A failed profile read leaves the plan unnamed or named by the allowance, and the limits stay;
// one billing read failing leaves the other's limits.
func TestQuotaPartial(t *testing.T) {
	srv := xaitest.New(t)
	srv.Billing = func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/billing" && r.URL.Query().Get("format") == "credits":
			http.Error(w, "no", http.StatusNotFound)
		case r.URL.Path == "/v1/billing":
			w.Write([]byte(`{"config":{"monthlyLimit":{"val":150000},"used":{"val":30000},"onDemandCap":{"val":0}}}`))
		default:
			http.Error(w, "no", http.StatusNotFound)
		}
	}
	a, _ := srv.Grant()
	q, err := quotaOf(t, srv, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: a})
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "SuperGrok Heavy" || len(q.Limits) != 1 {
		t.Fatalf("plan %q limits %+v", q.Plan, q.Limits)
	}
	m := limitByID(t, q, "monthly_credits")
	if !same(m.UsedPercent, f(20)) || !same(m.Remaining, f(1200)) || m.ResetsAt != 0 {
		t.Errorf("monthly %+v", m)
	}
}

// A login's grant names who signed in by the ID token's subject and e-mail address.
func TestLoginIdentity(t *testing.T) {
	srv := xaitest.New(t)
	set, _ := readSettings(srv.Settings())
	g, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identity) != 2 || g.Identity["email"] != xaitest.Email || g.Identity["sub"] != "user-1" {
		t.Errorf("identity %v", g.Identity)
	}
	for k, v := range g.Identity {
		if strings.Contains(v, g.Credential.AccessToken) || strings.Contains(v, g.Credential.RefreshToken) || k == "" {
			t.Errorf("identity %q holds a secret", k)
		}
	}
}
