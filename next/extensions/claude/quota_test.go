package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/claude/claudetest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func subscriptionAccount(srv *claudetest.Server) protocol.Account {
	access, refresh := srv.Grant()
	return protocol.Account{ID: "claude", Settings: srv.Settings(""),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh}}
}

func readQuota(t *testing.T, acct protocol.Account) (*protocol.Quota, *llm.Error) {
	t.Helper()
	q, err := quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
	var e *llm.Error
	if err != nil && !errors.As(err, &e) {
		t.Fatalf("quota returned %v, want nil or an *llm.Error", err)
	}
	return q, e
}

func limitsByID(t *testing.T, q *protocol.Quota) map[string]protocol.QuotaLimit {
	t.Helper()
	m := map[string]protocol.QuotaLimit{}
	for _, l := range q.Limits {
		if _, dup := m[l.ID]; dup {
			t.Errorf("limit id %q is repeated", l.ID)
		}
		m[l.ID] = l
	}
	return m
}

func at(t *testing.T, s string) int64 {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v.Unix()
}

func near(p *float64, want float64) bool { return p != nil && *p == want }

// A subscription's quota is read from the usage and profile endpoints, as the extension's own
// requests are made (bearer token, OAuth beta, API version), and mapped to the old panel's labels.
func TestQuotaReadsUsageAndProfile(t *testing.T) {
	srv := claudetest.New(t)
	acct := subscriptionAccount(srv)
	q, e := readQuota(t, acct)
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}

	reqs := srv.OAuthRequests()
	if len(reqs) != 2 || reqs[0].Prefix != "/api/oauth/usage" || reqs[1].Prefix != "/api/oauth/profile" {
		t.Fatalf("requests = %+v, want GET /api/oauth/usage and /api/oauth/profile", reqs)
	}
	for _, r := range reqs {
		for name, want := range map[string]string{
			"Authorization":     "Bearer " + acct.Credential.AccessToken,
			"Anthropic-Beta":    oauthBeta,
			"Anthropic-Version": anthropicVersion,
			"Accept":            "application/json",
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s %s = %q, want %q", r.Prefix, name, got, want)
			}
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Errorf("%s sent an API key header", r.Prefix)
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("the fake saw %d Messages requests, want none (a quota read must not generate)", n)
	}

	if q.Plan != "Max 5x" {
		t.Errorf("plan = %q, want Max 5x", q.Plan)
	}
	ids := []string{}
	for _, l := range q.Limits {
		ids = append(ids, l.ID)
	}
	if got, want := strings.Join(ids, ","), "five_hour,seven_day,seven_day_sonnet,extra_usage"; got != want {
		t.Fatalf("limit ids = %s, want %s (windows the reply leaves null are left out)", got, want)
	}
	l := limitsByID(t, q)
	if x := l["five_hour"]; x.Label != "5-hour limit" || !near(x.UsedPercent, 37) || x.ResetsAt != at(t, "2026-11-09T15:00:00Z") || x.WindowSeconds != 18000 {
		t.Errorf("five_hour = %+v", x)
	}
	if x := l["seven_day"]; x.Label != "Weekly limit" || !near(x.UsedPercent, 12.5) || x.ResetsAt != at(t, "2026-11-14T09:00:00Z") || x.WindowSeconds != 604800 {
		t.Errorf("seven_day = %+v", x)
	}
	if x := l["seven_day_sonnet"]; x.Label != "Weekly Sonnet limit" || !near(x.UsedPercent, 0) {
		t.Errorf("seven_day_sonnet = %+v", x)
	}
	// Extra usage is in cents: 50.00 a month, 12.50 spent.
	if x := l["extra_usage"]; x.Label != "Extra usage" || !near(x.UsedPercent, 25) || !near(x.Limit, 50) || !near(x.Used, 12.5) || !near(x.Remaining, 37.5) || x.Unit != "usd" {
		t.Errorf("extra_usage = %+v", x)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(q.Raw, &raw); err != nil || len(raw["usage"]) == 0 || len(raw["profile"]) == 0 {
		t.Errorf("raw = %s (%v), want the usage and profile replies", q.Raw, err)
	}
}

// An API key has no subscription plan; nothing is sent upstream.
func TestQuotaAPIKeyIsUnsupported(t *testing.T) {
	srv := claudetest.New(t)
	_, e := readQuota(t, protocol.Account{ID: "claude", Settings: srv.Settings(""),
		Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}})
	if e == nil || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("error = %+v, want code unsupported, scope credential", e)
	}
	if n := len(srv.OAuthRequests()) + len(srv.Requests()); n != 0 {
		t.Errorf("the fake saw %d requests, want none", n)
	}
}

// An expired token is an auth failure of the credential.
func TestQuotaExpiredTokenIsAuthError(t *testing.T) {
	srv := claudetest.New(t)
	acct := subscriptionAccount(srv)
	srv.Revoke(acct.Credential.AccessToken)
	_, e := readQuota(t, acct)
	if e == nil || e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || e.Status != http.StatusUnauthorized {
		t.Errorf("error = %+v, want code auth, scope credential, status 401", e)
	}
}

// Upstream failures are mapped as for the extension's other requests.
func TestQuotaUpstreamFailures(t *testing.T) {
	for _, c := range []struct {
		name      string
		status    int
		code      string
		scope     string
		retryable bool
		waitMS    int64
	}{
		{"rate limited", 429, llm.CodeRateLimit, llm.ScopeCredential, true, 20000},
		{"overloaded", 529, llm.CodeUnavailable, llm.ScopeEndpoint, true, 0},
		{"server error", 500, llm.CodeUpstream, llm.ScopeEndpoint, false, 0},
		{"endpoint missing", 404, llm.CodeNotFound, llm.ScopeEndpoint, false, 0},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"no"}}`))
		}))
		settings, _ := json.Marshal(map[string]string{"base_url": ts.URL})
		_, e := readQuota(t, protocol.Account{ID: "claude", Settings: settings, Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "tok"}})
		ts.Close()
		if e == nil || e.Code != c.code || e.Scope != c.scope || e.Retryable != c.retryable || (c.waitMS != 0 && e.RetryAfterMS != c.waitMS) {
			t.Errorf("%s: error = %+v, want code %s, scope %s, retryable %v, wait %d ms", c.name, e, c.code, c.scope, c.retryable, c.waitMS)
		}
	}

	srv := claudetest.New(t)
	acct := subscriptionAccount(srv)
	srv.Close()
	if _, e := readQuota(t, acct); e == nil || e.Code != llm.CodeUnavailable || !e.Retryable {
		t.Errorf("unreachable: error = %+v, want code unavailable, retryable", e)
	}
}

// A reply that is not JSON is an upstream fault, not an empty quota.
func TestQuotaBadUsageReply(t *testing.T) {
	for name, body := range map[string]string{"not JSON": `<html>`, "not an object": `[1]`, "null": `null`} {
		srv := claudetest.New(t)
		srv.Usage = body
		if _, e := readQuota(t, subscriptionAccount(srv)); e == nil || e.Code != llm.CodeUpstream {
			t.Errorf("%s: error = %+v, want code upstream", name, e)
		}
	}
}

// When the profile cannot be read the usage is still reported, without a plan.
func TestQuotaWithoutProfile(t *testing.T) {
	srv := claudetest.New(t)
	srv.ProfileStatus = http.StatusInternalServerError
	q, e := readQuota(t, subscriptionAccount(srv))
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	if q.Plan != "" || len(q.Limits) != 4 {
		t.Errorf("plan %q, %d limits; want no plan and the 4 limits of the usage", q.Plan, len(q.Limits))
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(q.Raw, &raw)
	if _, has := raw["profile"]; has || len(raw["usage"]) == 0 {
		t.Errorf("raw = %s, want the usage reply only", q.Raw)
	}
}

// A credit pool counted in dollars keeps its amounts; Fable's window comes from the reply's list of
// scoped limits when the reply has one; extra usage that is switched off is left out.
func TestQuotaOtherWindows(t *testing.T) {
	srv := claudetest.New(t)
	srv.Usage = `{
	  "five_hour": {"utilization": "20", "resets_at": null},
	  "seven_day_cowork": {"utilization": 5, "resets_at": "2026-11-14T09:00:00Z"},
	  "iguana_necktie": {"utilization": 8, "resets_at": "2026-11-20T00:00:00Z", "limit_dollars": 100, "used_dollars": 8, "remaining_dollars": 92},
	  "monthly_pool": {"utilization": 1, "resets_at": "2026-12-01T00:00:00Z", "limit_dollars": 10, "used_dollars": 0.1, "remaining_dollars": 9.9},
	  "unrelated": {"something": 1},
	  "extra_usage": {"is_enabled": false, "monthly_limit": null, "used_credits": null, "utilization": null},
	  "limits": [{"kind": "weekly_scoped", "percent": 33, "resets_at": "2026-11-15T00:00:00Z", "is_active": true, "scope": {"model": {"display_name": "Fable"}}}]
	}`
	q, e := readQuota(t, subscriptionAccount(srv))
	if e != nil {
		t.Fatalf("quota failed: %+v", e)
	}
	l := limitsByID(t, q)
	if len(l) != 5 {
		t.Fatalf("limits = %+v, want 5", q.Limits)
	}
	if x := l["five_hour"]; !near(x.UsedPercent, 20) || x.ResetsAt != 0 {
		t.Errorf("five_hour = %+v, want 20%% and no reset time", x)
	}
	if x := l["seven_day_cowork"]; x.Label != "Weekly Cowork limit" || !near(x.UsedPercent, 5) {
		t.Errorf("seven_day_cowork = %+v", x)
	}
	if x := l["cloud_session_credits"]; x.Label != "Cloud session credits" || x.Unit != "usd" || !near(x.Limit, 100) || !near(x.Used, 8) || !near(x.Remaining, 92) || !near(x.UsedPercent, 8) {
		t.Errorf("cloud_session_credits = %+v", x)
	}
	if x := l["monthly_pool"]; x.Label != "monthly_pool" || x.Unit != "usd" || !near(x.Remaining, 9.9) {
		t.Errorf("monthly_pool = %+v", x)
	}
	if x := l["seven_day_fable"]; x.Label != "Weekly Fable limit" || !near(x.UsedPercent, 33) || x.ResetsAt != at(t, "2026-11-15T00:00:00Z") || x.WindowSeconds != 604800 {
		t.Errorf("seven_day_fable = %+v", x)
	}
}

func TestPlanOf(t *testing.T) {
	for _, c := range []struct{ name, profile, want string }{
		{"team", `{"organization":{"organization_type":"claude_team","subscription_status":"active"},"account":{"has_claude_pro":true}}`, "Team"},
		{"max 20x", `{"organization":{"rate_limit_tier":"default_claude_max_20x"},"account":{"has_claude_max":true}}`, "Max 20x"},
		{"max 5x", `{"organization":{"rate_limit_tier":"default_claude_max_5x"}}`, "Max 5x"},
		{"max", `{"account":{"has_claude_max":true,"has_claude_pro":true}}`, "Max"},
		{"pro", `{"account":{"has_claude_max":false,"has_claude_pro":"true"}}`, "Pro"},
		{"free", `{"account":{"has_claude_max":false,"has_claude_pro":false}}`, "Free"},
		{"unknown", `{"account":{}}`, ""},
		{"not an object", `[]`, ""},
	} {
		if got := planOf(json.RawMessage(c.profile)); got != c.want {
			t.Errorf("%s: plan = %q, want %q", c.name, got, c.want)
		}
	}
}

// A subscription login names the upstream account by e-mail address, account ID and organization
// ID, read from the token endpoint's reply; fields it does not give are left out.
func TestLoginGrantsCarryIdentity(t *testing.T) {
	srv := claudetest.New(t)
	acct := subscriptionAccount(srv)
	var tr tokenResponse
	if f := tokenPost(context.Background(), srv.URL+"/v1/oauth/token", map[string]string{
		"grant_type": "refresh_token", "refresh_token": acct.Credential.RefreshToken, "client_id": claudetest.ClientID,
	}, &tr); f != nil {
		t.Fatalf("token request failed: %+v", f)
	}
	want := map[string]string{"email": claudetest.Email, "account_id": claudetest.AccountUUID, "organization_id": claudetest.OrganizationUUID}
	got := tr.identity()
	if len(got) != len(want) {
		t.Errorf("identity = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("identity[%s] = %q, want %q", k, got[k], v)
		}
	}
	for k, v := range got {
		if v == tr.AccessToken || v == tr.RefreshToken {
			t.Errorf("identity[%s] is a token", k)
		}
	}

	var bare tokenResponse
	bare.Account.EmailAddress = "someone@example.invalid"
	if got := bare.identity(); len(got) != 1 || got["email"] != "someone@example.invalid" {
		t.Errorf("identity of a reply with only an e-mail address = %v", got)
	}
}
