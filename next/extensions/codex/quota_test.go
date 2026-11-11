package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func readQuota(t *testing.T, acct protocol.Account) (*protocol.Quota, *llm.Error) {
	t.Helper()
	q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
	var e *llm.Error
	if err != nil && !errors.As(err, &e) {
		t.Fatalf("quota returned %v, want nil or an *llm.Error", err)
	}
	return q, e
}

func usageRequests(srv *codextest.Server) []codextest.Request {
	var out []codextest.Request
	for _, r := range srv.Requests() {
		if strings.HasSuffix(r.Path, "/wham/usage") {
			out = append(out, r)
		}
	}
	return out
}

func pct(v float64) *float64 { return &v }

func TestQuotaOptionalReadsFailIndependently(t *testing.T) {
	for _, c := range []struct {
		name, subscriptions, credits string
		end                          int64
		reset                        bool
	}{
		{"subscription unavailable", "", `{"available_count":2}`, 0, true},
		{"credits unavailable", `{"active_until":"2031-01-01T00:00:00Z"}`, "", 1924992000, false},
		{"malformed", "<html>", "<html>", 0, false},
		{"invalid fields", `{"active_until":"invalid"}`, `{"available_count":-1}`, 0, false},
		{"absent fields", `{}`, `{}`, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := codextest.New(t)
			srv.Subscriptions, srv.ResetCredits = c.subscriptions, c.credits
			q, e := readQuota(t, testAccount(srv, subscription(srv.IssueToken("text"), "")))
			if e != nil {
				t.Fatalf("quota failed: %+v", e)
			}
			b, _ := json.Marshal(q)
			var result struct {
				PlanEndsAt int64 `json:"plan_ends_at"`
			}
			_ = json.Unmarshal(b, &result)
			if result.PlanEndsAt != c.end {
				t.Errorf("plan_ends_at = %d, want %d", result.PlanEndsAt, c.end)
			}
			found := false
			for _, l := range q.Limits {
				if l.ID == "reset_credits" {
					found = true
					if l.Remaining == nil || *l.Remaining != 2 {
						t.Errorf("reset credits = %+v, want 2", l)
					}
				}
			}
			if found != c.reset {
				t.Errorf("reset credits present = %v, want %v", found, c.reset)
			}
		})
	}
}

func TestQuotaRenewalAndManualResets(t *testing.T) {
	srv := codextest.New(t)
	srv.Subscriptions = `{"active_until":"2031-01-01T00:00:00Z"}`
	srv.ResetCredits = `{"available_count":0,"credits":[]}`
	token := srv.IssueToken("text")
	q, e := readQuota(t, testAccount(srv, subscription(token, "")))
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	requests := srv.Requests()
	if len(requests) != 3 {
		t.Fatalf("quota requests = %d, want usage, subscriptions and reset credits", len(requests))
	}
	for i, path := range []string{"/backend-api/wham/usage", "/backend-api/subscriptions?account_id=" + codextest.AccountID, "/backend-api/wham/rate-limit-reset-credits"} {
		if requests[i].Path != path {
			t.Errorf("request %d path = %q, want %q", i, requests[i].Path, path)
		}
		for name, want := range map[string]string{"Authorization": "Bearer " + token, "Chatgpt-Account-Id": codextest.AccountID, "User-Agent": codexUserAgent, "Originator": codexOriginator, "OpenAI-Beta": ""} {
			if got := requests[i].Header.Get(name); got != want {
				t.Errorf("request %d %s = %q, want %q", i, name, got, want)
			}
		}
	}
	b, _ := json.Marshal(q)
	var result struct {
		PlanEndsAt int64 `json:"plan_ends_at"`
	}
	if err := json.Unmarshal(b, &result); err != nil || result.PlanEndsAt != 1924992000 {
		t.Errorf("quota = %s, want plan_ends_at 1924992000", b)
	}
	found := false
	for _, l := range q.Limits {
		if l.ID == "reset_credits" {
			found = true
			if l.Label != "Manual resets" || l.Remaining == nil || *l.Remaining != 0 || l.Unit != "resets" {
				t.Errorf("manual resets = %+v", l)
			}
		}
	}
	if !found {
		t.Error("quota has no reset_credits limit")
	}
}

// A subscription's quota is read from the ChatGPT backend's usage endpoint, with the Codex client's
// identity and the account's ChatGPT ID, and mapped to the labels of the old panel.
func TestQuotaReadsUsage(t *testing.T) {
	srv := codextest.New(t)
	token := srv.IssueToken("text")
	q, e := readQuota(t, testAccount(srv, subscription(token, "")))
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	reqs := usageRequests(srv)
	if len(reqs) != 1 {
		t.Fatalf("usage requests = %d, want 1", len(reqs))
	}
	h := reqs[0].Header
	// The fake serves GET only, so a request with another method would have been refused.
	if reqs[0].Path != "/backend-api/wham/usage" {
		t.Errorf("path = %q, want /backend-api/wham/usage (the base URL without /codex)", reqs[0].Path)
	}
	for name, want := range map[string]string{
		"Authorization":      "Bearer " + token,
		"Chatgpt-Account-Id": codextest.AccountID,
		"User-Agent":         codexUserAgent,
		"Originator":         codexOriginator,
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if n := len(srv.Requests()); n != 3 {
		t.Errorf("the fake saw %d requests, want three quota reads (a quota read must not generate)", n)
	}

	if q.Plan != "plus" {
		t.Errorf("plan = %q, want plus", q.Plan)
	}
	type want struct {
		id, label string
		used      *float64
		resets    int64
		window    int64
	}
	wants := []want{
		{"five_hour", "5-hour limit", pct(42), 1794268800, 18000},
		{"weekly", "Weekly limit", pct(7.5), 1794668800, 604800},
		{"code_review_weekly", "Code review weekly limit", pct(0), 1794768800, 604800},
		{"gpt_5_3_codex_spark_five_hour", "GPT-5.3-Codex-Spark 5-hour limit", pct(10), 1794260000, 18000},
		{"gpt_5_3_codex_spark_weekly", "GPT-5.3-Codex-Spark weekly limit", pct(3), 1794270000, 604800},
	}
	if len(q.Limits) != len(wants)+1 {
		t.Fatalf("limits = %+v, want %d windows and the credit balance", q.Limits, len(wants))
	}
	for i, w := range wants {
		l := q.Limits[i]
		if l.ID != w.id || l.Label != w.label || l.UsedPercent == nil || *l.UsedPercent != *w.used || l.ResetsAt != w.resets || l.WindowSeconds != w.window {
			t.Errorf("limits[%d] = %+v, want %+v", i, l, w)
		}
	}
	c := q.Limits[len(wants)]
	if c.ID != "credits" || c.Label != "Credit balance" || c.Remaining == nil || *c.Remaining != 12.5 || c.Unit != "credits" || c.Unlimited {
		t.Errorf("credit limit = %+v, want a balance of 12.5 credits", c)
	}
	var raw struct {
		Usage struct {
			PlanType string `json:"plan_type"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(q.Raw, &raw); err != nil || raw.Usage.PlanType != "plus" {
		t.Errorf("raw = %s (%v), want the usage reply under \"usage\"", q.Raw, err)
	}
}

// With codex_identity off, the extension sends no Codex User-Agent, as for its requests.
func TestQuotaFollowsCodexIdentitySetting(t *testing.T) {
	srv := codextest.New(t)
	acct := testAccount(srv, subscription(srv.IssueToken("text"), ""))
	acct.Settings, _ = json.Marshal(map[string]any{"base_url": srv.URL + "/backend-api/codex/", "codex_identity": false})
	if _, e := readQuota(t, acct); e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	h := usageRequests(srv)[0].Header
	if strings.Contains(h.Get("User-Agent"), "codex") {
		t.Errorf("User-Agent = %q, want none of the Codex client's", h.Get("User-Agent"))
	}
	if h.Get("Originator") != codexOriginator || h.Get("Chatgpt-Account-Id") != codextest.AccountID {
		t.Errorf("Originator %q, Chatgpt-Account-Id %q; want %q and %q", h.Get("Originator"), h.Get("Chatgpt-Account-Id"), codexOriginator, codextest.AccountID)
	}
}

// An API key has no ChatGPT plan; nothing is sent upstream.
func TestQuotaAPIKeyIsUnsupported(t *testing.T) {
	srv := codextest.New(t)
	_, e := readQuota(t, testAccount(srv, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-test"}))
	if e == nil || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("error = %+v, want code unsupported, scope credential", e)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("the fake saw %d requests, want none", n)
	}
}

func TestQuotaErrors(t *testing.T) {
	srv := codextest.New(t)
	for _, c := range []struct {
		name      string
		token     string
		code      string
		retryable bool
		waitMS    int64
	}{
		{"expired token", "not-issued", llm.CodeAuth, true, 0},
		{"rate limited", srv.IssueToken("rate_limited"), llm.CodeRateLimit, true, 20000},
	} {
		_, e := readQuota(t, testAccount(srv, subscription(c.token, "")))
		if e == nil || e.Code != c.code || e.Scope != llm.ScopeCredential || e.Retryable != c.retryable || e.RetryAfterMS != c.waitMS {
			t.Errorf("%s: error = %+v, want code %s, scope credential, retryable %v, wait %d ms", c.name, e, c.code, c.retryable, c.waitMS)
		}
	}

	acct := testAccount(srv, subscription(srv.IssueToken("text"), ""))
	srv.Close()
	if _, e := readQuota(t, acct); e == nil || e.Code != llm.CodeUnavailable || !e.Retryable {
		t.Errorf("unreachable: error = %+v, want code unavailable, retryable", e)
	}
}

func TestQuotaBadReplies(t *testing.T) {
	for name, body := range map[string]string{"not JSON": `<html>`, "not an object": `[1]`} {
		srv := codextest.New(t)
		srv.Usage = body
		if _, e := readQuota(t, testAccount(srv, subscription(srv.IssueToken("text"), ""))); e == nil || e.Code != llm.CodeUpstream {
			t.Errorf("%s: error = %+v, want code upstream", name, e)
		}
	}
}

// Windows are told apart by their length (a monthly one is labelled so), a window with no length
// is taken by its place, a used-up rate limit without a share counts as used, and a reset may be
// given as seconds from now or in milliseconds.
func TestQuotaWindows(t *testing.T) {
	const body = `{
	  "plan_type": "team",
	  "rate_limit": {
	    "allowed": false, "limit_reached": true,
	    "primary_window": {"limit_window_seconds": 18000, "reset_after_seconds": 600},
	    "secondary_window": {"used_percent": "55", "limit_window_seconds": 2592000, "reset_at": 1794268800000}
	  },
	  "code_review_rate_limit": {
	    "primary_window": {"used_percent": 140},
	    "secondary_window": {"used_percent": 5}
	  },
	  "credits": {"has_credits": true, "unlimited": true, "balance": null}
	}`
	srv := codextest.New(t)
	srv.Usage = body
	start := time.Now().Unix()
	q, e := readQuota(t, testAccount(srv, subscription(srv.IssueToken("text"), "")))
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	got := map[string]protocol.QuotaLimit{}
	for _, l := range q.Limits {
		got[l.ID] = l
	}
	if len(got) != len(q.Limits) || len(got) != 5 {
		t.Fatalf("limits = %+v, want 5 with distinct ids", q.Limits)
	}
	if l := got["five_hour"]; l.UsedPercent == nil || *l.UsedPercent != 100 || l.ResetsAt < start+600 || l.ResetsAt > start+610 {
		t.Errorf("five_hour = %+v, want 100%% used (limit reached), resetting in 600 s", l)
	}
	if l := got["monthly"]; l.Label != "Monthly limit" || l.UsedPercent == nil || *l.UsedPercent != 55 || l.ResetsAt != 1794268800 || l.WindowSeconds != 2592000 {
		t.Errorf("monthly = %+v, want 55%% used, reset at 1794268800", l)
	}
	if l := got["code_review_five_hour"]; l.UsedPercent == nil || *l.UsedPercent != 100 {
		t.Errorf("code_review_five_hour = %+v, want a share clamped to 100", l)
	}
	if l := got["code_review_weekly"]; l.UsedPercent == nil || *l.UsedPercent != 5 {
		t.Errorf("code_review_weekly = %+v, want 5%% used", l)
	}
	if l := got["credits"]; !l.Unlimited || l.Remaining != nil {
		t.Errorf("credits = %+v, want unlimited", l)
	}
	if q.Plan != "team" {
		t.Errorf("plan = %q, want team", q.Plan)
	}
}

// A browser, device or renewed login names the upstream account by e-mail address and ChatGPT
// account ID, which workspace members share; an API key names none.
func TestLoginGrantsCarryIdentity(t *testing.T) {
	g, err := grantFrom(&tokenResponse{AccessToken: "opaque", RefreshToken: "r", IDToken: codextest.IDToken()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"email": codextest.Email, "account_id": codextest.AccountID}
	if len(g.Identity) != len(want) || g.Identity["email"] != want["email"] || g.Identity["account_id"] != want["account_id"] {
		t.Errorf("identity = %v, want %v", g.Identity, want)
	}
	if g.Label != codextest.Email {
		t.Errorf("label = %q, want the e-mail address as before", g.Label)
	}
	for k, v := range g.Identity {
		if strings.Contains(v, "opaque") || strings.Contains(k, "token") {
			t.Errorf("identity %q: %q looks like a secret", k, v)
		}
	}

	srv := codextest.New(t)
	k, err := login(context.Background(), sdk.LoginCall{Account: testAccount(srv, nil), Method: "api_key", Input: map[string]string{"api_key": "sk-test"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Identity) != 0 {
		t.Errorf("an API key login names an identity: %v", k.Identity)
	}
}
