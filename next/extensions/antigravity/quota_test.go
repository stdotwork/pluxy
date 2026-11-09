package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/antigravity/agtest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func quotaAccount(srv *agtest.Server, access string) protocol.Account {
	return protocol.Account{ID: "ag", Settings: srv.Settings(), Credential: signedIn(access, "r")}
}

func readQuota(acct protocol.Account) (*protocol.Quota, error) {
	return quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
}

func pf(v float64) *float64 { return &v }

func unix(y int, m time.Month, d, h int) int64 {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC).Unix()
}

func TestQuotaReadsSummaryAndPlan(t *testing.T) {
	srv := agtest.New(t)
	srv.PaidTier = "g1-pro-tier"
	access, _ := srv.Grant()
	q, err := readQuota(quotaAccount(srv, access))
	if err != nil {
		t.Fatal(err)
	}

	var quotaReq, planReq *agtest.Request
	reqs := srv.Requests()
	for i := range reqs {
		switch reqs[i].Path {
		case "/v1internal:retrieveUserQuotaSummary":
			quotaReq = &reqs[i]
		case "/v1internal:loadCodeAssist":
			planReq = &reqs[i]
		}
	}
	if len(reqs) != 2 || quotaReq == nil || planReq == nil {
		t.Fatalf("requests = %+v, want retrieveUserQuotaSummary and loadCodeAssist", reqs)
	}
	for _, r := range reqs {
		if r.Header.Get("Authorization") != "Bearer "+access || r.Header.Get("User-Agent") != userAgent || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s headers = %v, want the bearer token, the Antigravity User-Agent and JSON", r.Path, r.Header)
		}
	}
	var body struct{ Project string }
	if err := json.Unmarshal(quotaReq.Body, &body); err != nil || body.Project != agtest.Project {
		t.Errorf("quota request body = %s, want the credential's project %q", quotaReq.Body, agtest.Project)
	}

	if q.Plan != "Pro" {
		t.Errorf("plan = %q, want Pro (the paid tier)", q.Plan)
	}
	models := []string{"Gemini 3 Pro", "Gemini 3 Flash"}
	want := []protocol.QuotaLimit{
		{ID: "gemini_models_five_hour", Label: "Gemini models 5-hour limit", UsedPercent: pf(60), ResetsAt: unix(2026, 11, 9, 15), WindowSeconds: 18000, Models: models},
		{ID: "gemini_models_weekly", Label: "Gemini models weekly limit", UsedPercent: pf(25), ResetsAt: unix(2026, 11, 14, 0), WindowSeconds: 604800, Models: models},
		{ID: "claude_and_gpt_models_weekly", Label: "Claude and GPT models weekly limit", UsedPercent: pf(0), ResetsAt: unix(2026, 11, 14, 0), WindowSeconds: 604800,
			Models: []string{"Claude Sonnet 4.6", "GPT-OSS 120B"}},
	}
	if !reflect.DeepEqual(q.Limits, want) {
		got, _ := json.Marshal(q.Limits)
		exp, _ := json.Marshal(want)
		t.Errorf("limits:\n got %s\nwant %s", got, exp)
	}
	var raw struct{ Usage, Profile json.RawMessage }
	if err := json.Unmarshal(q.Raw, &raw); err != nil || len(raw.Usage) == 0 || len(raw.Profile) == 0 {
		t.Errorf("raw = %s (%v), want the quota and the code assist replies", q.Raw, err)
	}
}

func TestQuotaPlanFromCurrentTier(t *testing.T) {
	srv := agtest.New(t)
	access, _ := srv.Grant()
	q, err := readQuota(quotaAccount(srv, access))
	if err != nil || q.Plan != "Free" {
		t.Errorf("plan %q, err %v; want Free", q.Plan, err)
	}
}

// The account's configured User-Agent and Cloud Code host are the ones the quota read uses.
func TestQuotaUsesAccountSettings(t *testing.T) {
	srv := agtest.New(t)
	access, _ := srv.Grant()
	acct := quotaAccount(srv, access)
	acct.Settings, _ = json.Marshal(map[string]any{"base_url": srv.URL + "/", "auth_base_url": srv.URL, "user_agent": "custom-agent/9"})
	if _, err := readQuota(acct); err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if r.Header.Get("User-Agent") != "custom-agent/9" {
			t.Errorf("%s sent User-Agent %q, want the account's", r.Path, r.Header.Get("User-Agent"))
		}
	}
}

func TestQuotaWithoutPlan(t *testing.T) {
	srv := agtest.New(t)
	srv.ProfileStatus = 500
	access, _ := srv.Grant()
	q, err := readQuota(quotaAccount(srv, access))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if q.Plan != "" || len(q.Limits) != 3 || json.Unmarshal(q.Raw, &raw) != nil || raw["usage"] == nil || raw["profile"] != nil {
		t.Errorf("plan %q, %d limits, raw %s; want no plan, 3 limits and only the quota reply", q.Plan, len(q.Limits), q.Raw)
	}
}

func TestQuotaErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*agtest.Server, string)
		code  string
		scope string
	}{
		{"revoked token", func(s *agtest.Server, a string) { s.Revoke(a) }, llm.CodeAuth, llm.ScopeCredential},
		{"forbidden", func(s *agtest.Server, _ string) { s.QuotaStatus = 403 }, llm.CodePermission, llm.ScopeCredential},
		{"rate limited", func(s *agtest.Server, _ string) { s.QuotaStatus = 429 }, llm.CodeRateLimit, llm.ScopeCredential},
		{"server error", func(s *agtest.Server, _ string) { s.QuotaStatus = 500 }, llm.CodeUpstream, llm.ScopeEndpoint},
		{"not found", func(s *agtest.Server, _ string) { s.QuotaStatus = 404 }, llm.CodeUpstream, llm.ScopeEndpoint},
		{"not JSON", func(s *agtest.Server, _ string) { s.QuotaSummary = "<html>" }, llm.CodeUpstream, llm.ScopeEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := agtest.New(t)
			access, _ := srv.Grant()
			tc.setup(srv, access)
			_, err := readQuota(quotaAccount(srv, access))
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != tc.code || e.Scope != tc.scope {
				t.Errorf("err = %+v, want %s with scope %s", err, tc.code, tc.scope)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		srv := agtest.New(t)
		access, _ := srv.Grant()
		acct := quotaAccount(srv, access)
		srv.Close()
		_, err := readQuota(acct)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("err = %+v, want a retryable unavailable error", err)
		}
	})
}

func TestQuotaNeedsALoginWithAProject(t *testing.T) {
	srv := agtest.New(t)
	access, _ := srv.Grant()
	noProject := signedIn(access, "r")
	noProject.Data = []byte(`{}`)
	apiKey := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: access}
	for _, tc := range []struct {
		name string
		c    *protocol.Credential
		code string
	}{{"no project", noProject, llm.CodeAuth}, {"no credential", nil, llm.CodeAuth}, {"api key", apiKey, llm.CodeUnsupported}} {
		_, err := readQuota(protocol.Account{ID: "ag", Settings: srv.Settings(), Credential: tc.c})
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != tc.code || e.Scope != llm.ScopeCredential {
			t.Errorf("%s: err = %+v, want %s with scope credential", tc.name, err, tc.code)
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("%d requests were made for accounts with nothing to read, want none", n)
	}
}

func TestQuotaSummaryLimits(t *testing.T) {
	var qs quotaSummary
	if err := json.Unmarshal([]byte(`{"groups":[
		{"buckets":[
			{"bucketId":"b1","displayName":"Monthly Limit","remainingFraction":2},
			{"bucketId":"b2","displayName":"Credits","remaining_fraction":"-1","reset_time":"bad"},
			{"displayName":"No share"},
			{"bucketId":"b4","description":"Described","window":"hourly","remainingFraction":"0.5"},
			{"bucketId":"b5","displayName":"Daily limit remaining","remainingFraction":0.9}]},
		{"display_name":"Other models","description":"Not a model list","buckets":[
			{"window":"week","remainingFraction":0.2},{"window":"week","remainingFraction":0.3}]},
		{"displayName":"Empty","buckets":[{"remainingFraction":"x"}]}]}`), &qs); err != nil {
		t.Fatal(err)
	}
	got := qs.limits()
	type row struct {
		id, label string
		used      float64
		window    int64
	}
	var rows []row
	for _, l := range got {
		rows = append(rows, row{l.ID, l.Label, *l.UsedPercent, l.WindowSeconds})
	}
	want := []row{
		// Unnamed groups are numbered; the 5-hour and weekly windows come first, the rest by label.
		{"quota_group_1_b2", "Quota Group 1 Credits", 100, 0},
		{"quota_group_1_daily", "Quota Group 1 daily limit", 10, 86400},
		{"quota_group_1_hourly", "Quota Group 1 Described", 50, 0},
		{"quota_group_1_monthly", "Quota Group 1 monthly limit", 0, 0},
		{"other_models_weekly", "Other models weekly limit", 80, 604800},
		{"other_models_weekly_2", "Other models weekly limit", 70, 604800},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("limits:\n got %+v\nwant %+v", rows, want)
	}
	for _, l := range got {
		if l.Models != nil {
			t.Errorf("%s has models %v, but its group names none", l.ID, l.Models)
		}
	}
}

// A login reports the Google account's e-mail address as the identity, besides the label.
func TestLoginGrantCarriesIdentity(t *testing.T) {
	srv := agtest.New(t)
	access, rt := srv.Grant()
	set, _ := readSettings(srv.Settings())
	g, err := finishLogin(context.Background(), set, &tokenResponse{AccessToken: access, RefreshToken: rt, ExpiresIn: 3600})
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]string{"email": agtest.Email}); !reflect.DeepEqual(g.Identity, want) {
		t.Errorf("identity = %v, want %v", g.Identity, want)
	}
	if g.Label != agtest.Email {
		t.Errorf("label = %q, want the e-mail address", g.Label)
	}
	b, _ := json.Marshal(g.Identity)
	for _, secret := range []string{access, rt, clientSecret} {
		if strings.Contains(string(b), secret) {
			t.Errorf("identity %s holds a secret", b)
		}
	}
}
