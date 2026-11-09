package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/meta/metatest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func quotaOf(t *testing.T, srv *metatest.Server, settings json.RawMessage, c *protocol.Credential) (*protocol.Quota, error) {
	t.Helper()
	t.Setenv(protocol.EnvTestLoginHosts, "1") // the fake's key endpoint stands in for Meta's
	if settings == nil {
		settings = srv.Settings()
	}
	return quota(context.Background(), sdk.QuotaCall{Account: protocol.Account{ID: "muse", Settings: settings, Credential: c}})
}

func llmErr(err error) *llm.Error {
	e, _ := err.(*llm.Error)
	return e
}

func pct(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

// A Muse Code login's quota is read with one POST to Meta's key endpoint (the fake's, in this test): an empty
// object as the body, the device token (not the minted key) as the bearer, the API version header
// and Muse Code's client identity. The plan and both windows are reported; the key the call mints
// and the person's details are in neither the limits nor the raw reply.
func TestQuota(t *testing.T) {
	srv := metatest.New(t)
	dca, key := srv.Grant()
	q, err := quotaOf(t, srv, nil, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca})
	if err != nil {
		t.Fatal(err)
	}
	calls := srv.KeyRequests()
	if len(calls) != 1 {
		t.Fatalf("%d key calls, want 1", len(calls))
	}
	h := calls[0].Header
	if string(calls[0].Body) != "{}" || h.Get("Authorization") != "Bearer "+dca || h.Get("x-api-version") != "1.0.0" ||
		h.Get("User-Agent") != userAgent() || h.Get("X-Client-Id") != museSurface || h.Get("X-Tbh-Session-Id") == "" || h.Get("X-Tbh-Agent-Role") != "main" {
		t.Errorf("key call body %q headers %v", calls[0].Body, h)
	}
	if q.Plan != "Muse Code" || len(q.Limits) != 2 {
		t.Fatalf("plan %q limits %+v", q.Plan, q.Limits)
	}
	w, wk := q.Limits[0], q.Limits[1]
	if w.ID != "window" || w.Label != "Usage limit" || pct(w.UsedPercent) != 12.5 || w.ResetsAt != 1794200000 || w.WindowSeconds != 18000 {
		t.Errorf("window %+v", w)
	}
	if wk.ID != "weekly" || wk.Label != "Weekly limit" || pct(wk.UsedPercent) != 40 || wk.ResetsAt != 1794700000 || wk.WindowSeconds != 0 {
		t.Errorf("weekly %+v", wk)
	}
	all, _ := json.Marshal(q)
	for _, secret := range []string{"api_key", "metatest-key-", "user_email", metatest.Email, "Test User"} {
		if strings.Contains(string(all), secret) {
			t.Errorf("the quota holds %q: %s", secret, all)
		}
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(q.Raw, &raw) != nil || raw["subs_usage"] == nil || raw["subs_tier_name"] == nil {
		t.Errorf("raw %s", q.Raw)
	}
	// The call mints a key but does not change the account's: its own key still works.
	if _, _, err := target(settings{}, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key}); err != nil {
		t.Fatal(err)
	}
}

// The device token goes only to the key endpoint a renewal uses, never to base_url's host, which
// an account may point at a relay; outside a test run that is Meta's own.
func TestQuotaKeyEndpointNotBaseURL(t *testing.T) {
	srv := metatest.New(t)
	dca, key := srv.Grant()
	elsewhere := `{"base_url":"` + srv.URL + `/v1","mint_url":"http://127.0.0.1:1/muse-code/key"}`
	_, err := quotaOf(t, srv, json.RawMessage(elsewhere), &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca})
	if e := llmErr(err); e == nil || e.Code != llm.CodeUnavailable {
		t.Errorf("error %#v, want unavailable from the unreachable key endpoint", err)
	}
	if n := len(srv.KeyRequests()); n != 0 {
		t.Errorf("%d key calls reached base_url's host, want 0", n)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "")
	s, err := readSettings(json.RawMessage(elsewhere))
	if err != nil || s.MintURL != defaultMintURL {
		t.Errorf("outside a test run the key endpoint is %q (%v), want %q", s.MintURL, err, defaultMintURL)
	}
}

// An API key has no plan to report, and makes no call.
func TestQuotaAPIKeyUnsupported(t *testing.T) {
	srv := metatest.New(t)
	_, err := quotaOf(t, srv, nil, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"})
	if e := llmErr(err); e == nil || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("error %#v", err)
	}
	if n := len(srv.KeyRequests()); n != 0 {
		t.Errorf("%d key calls for an API key", n)
	}
}

// A refused device token is auth with scope credential, whose message does not carry the endpoint's
// text; a rate limit keeps Retry-After; an outage is unavailable; an unreachable host is
// unavailable and retryable; a reply that is not JSON is an upstream error.
func TestQuotaErrors(t *testing.T) {
	cred := func(srv *metatest.Server) *protocol.Credential {
		dca, key := srv.Grant()
		return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca}
	}
	t.Run("401", func(t *testing.T) {
		srv := metatest.New(t)
		c := cred(srv)
		srv.Revoke(c.RefreshToken)
		_, err := quotaOf(t, srv, nil, c)
		e := llmErr(err)
		if e == nil || e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || e.Status != http.StatusUnauthorized || strings.Contains(e.Message, "Invalid device token") {
			t.Errorf("error %#v", err)
		}
	})
	t.Run("429", func(t *testing.T) {
		srv := metatest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"error":{"message":"api_key LLM|echoed"}}`, http.StatusTooManyRequests)
			return true
		}
		_, err := quotaOf(t, srv, nil, cred(srv))
		e := llmErr(err)
		if e == nil || e.Code != llm.CodeRateLimit || e.RetryAfterMS != 7000 || strings.Contains(e.Message, "LLM|echoed") {
			t.Errorf("error %#v", err)
		}
	})
	t.Run("500", func(t *testing.T) {
		srv := metatest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
			http.Error(w, "boom", http.StatusInternalServerError)
			return true
		}
		if e := llmErr(func() error { _, err := quotaOf(t, srv, nil, cred(srv)); return err }()); e == nil || e.Code != llm.CodeUpstream {
			t.Errorf("error %#v", e)
		}
	})
	t.Run("503", func(t *testing.T) {
		srv := metatest.New(t)
		srv.MintDown = true
		if e := llmErr(func() error { _, err := quotaOf(t, srv, nil, cred(srv)); return err }()); e == nil || e.Code != llm.CodeUnavailable {
			t.Errorf("error %#v", e)
		}
	})
	t.Run("not JSON", func(t *testing.T) {
		srv := metatest.New(t)
		srv.Answer = func(w http.ResponseWriter, r *http.Request) bool { w.Write([]byte("<html>")); return true }
		if e := llmErr(func() error { _, err := quotaOf(t, srv, nil, cred(srv)); return err }()); e == nil || e.Code != llm.CodeUpstream {
			t.Errorf("error %#v", e)
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		srv := metatest.New(t)
		c, settings := cred(srv), srv.Settings()
		srv.Close()
		if e := llmErr(func() error { _, err := quotaOf(t, srv, settings, c); return err }()); e == nil || e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("error %#v", e)
		}
	})
	t.Run("no device token", func(t *testing.T) {
		srv := metatest.New(t)
		_, err := quotaOf(t, srv, nil, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "metatest-key-1"})
		if e := llmErr(err); e == nil || e.Code != llm.CodeAuth {
			t.Errorf("error %#v", err)
		}
	})
}

// A reply with no usage yet (nothing requested) is a plan and no limits; out-of-range shares are
// clamped, and a window with nothing in it is left out.
func TestQuotaOddReplies(t *testing.T) {
	srv := metatest.New(t)
	srv.KeyReply = func(key string) any {
		return map[string]any{"api_key": key, "subs_usage": map[string]any{"tier": "Muse Pro", "window": map[string]any{"used_percent": -4, "resets_at": 0, "window_duration_mins": "bad"},
			"weekly": map[string]any{}}}
	}
	dca, key := srv.Grant()
	q, err := quotaOf(t, srv, nil, &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca})
	if err != nil {
		t.Fatal(err)
	}
	if q.Plan != "Muse Pro" || len(q.Limits) != 1 || q.Limits[0].ID != "window" || pct(q.Limits[0].UsedPercent) != 0 || q.Limits[0].WindowSeconds != 0 {
		t.Errorf("quota %+v", q)
	}
}

// A device login's grant names who signed in by the e-mail address the key endpoint reports; a
// reply with none gives no identity.
func TestLoginIdentity(t *testing.T) {
	old := waitPoll
	waitPoll = func(context.Context, time.Duration) bool { return true }
	defer func() { waitPoll = old }()
	srv := metatest.New(t)
	set, _ := readSettings(srv.Settings())
	g, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identity) != 1 || g.Identity["email"] != metatest.Email || g.Label != metatest.Email {
		t.Errorf("identity %v label %q", g.Identity, g.Label)
	}
	for _, v := range g.Identity {
		if strings.Contains(v, g.Credential.AccessToken) || strings.Contains(v, g.Credential.RefreshToken) {
			t.Error("identity holds a secret")
		}
	}
	srv.KeyReply = func(key string) any { return map[string]any{"api_key": key} }
	g, err = deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Identity) != 0 {
		t.Errorf("identity %v with no e-mail address", g.Identity)
	}
}
