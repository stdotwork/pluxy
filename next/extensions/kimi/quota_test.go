package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func quotaAccount(srv *kimitest.Server, access string) protocol.Account {
	return protocol.Account{ID: "kimi", Settings: srv.Settings(), Credential: &protocol.Credential{
		Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: "r", Data: []byte(`{"device_id":"dev-1"}`)}}
}

func readQuota(srv *kimitest.Server, acct protocol.Account) (*protocol.Quota, error) {
	return quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
}

func asLLMError(t *testing.T, err error) *llm.Error {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *llm.Error", err)
	}
	return e
}

func pf(v float64) *float64 { return &v }

func TestQuotaReadsUsageAndPlan(t *testing.T) {
	srv := kimitest.New(t)
	access, _ := srv.Grant()
	q, err := readQuota(srv, quotaAccount(srv, access))
	if err != nil {
		t.Fatal(err)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[0].Method != http.MethodGet || reqs[0].Path != "/coding/v1/usages" ||
		reqs[1].Method != http.MethodGet || reqs[1].Path != "/coding/v1/me" {
		t.Fatalf("requests = %+v, want GET /coding/v1/usages then GET /coding/v1/me", reqs)
	}
	for _, r := range reqs {
		h := r.Header
		if h.Get("Authorization") != "Bearer "+access || h.Get("X-Msh-Platform") != "pluxy" || h.Get("X-Msh-Version") != version ||
			h.Get("X-Msh-Device-Id") != "dev-1" || h.Get("X-Msh-Device-Model") == "" || h.Get("User-Agent") != "pluxy-kimi/"+version {
			t.Errorf("%s headers = %v, want the bearer token and the account's device headers", r.Path, h)
		}
	}

	if q.Plan != "Andante" {
		t.Errorf("plan = %q, want Andante", q.Plan)
	}
	want := []protocol.QuotaLimit{
		{ID: "limit_1", Label: "5-hour limit", UsedPercent: pf(20), Used: pf(10), Limit: pf(50), Remaining: pf(40),
			ResetsAt: time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC).Unix(), WindowSeconds: 18000},
		{ID: "usage", Label: "Weekly limit", UsedPercent: pf(40), Used: pf(40), Limit: pf(100), Remaining: pf(60),
			ResetsAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "month_total", Label: "Monthly limit", UsedPercent: pf(25), ResetsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "month_code", Label: "Monthly coding limit", UsedPercent: pf(50), ResetsAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Unix()},
	}
	if !reflect.DeepEqual(q.Limits, want) {
		got, _ := json.Marshal(q.Limits)
		exp, _ := json.Marshal(want)
		t.Errorf("limits:\n got %s\nwant %s", got, exp)
	}

	var raw struct {
		Usage, Profile json.RawMessage
	}
	if err := json.Unmarshal(q.Raw, &raw); err != nil || len(raw.Usage) == 0 || len(raw.Profile) == 0 {
		t.Errorf("raw = %s (%v), want the usage and profile replies", q.Raw, err)
	}
}

func TestQuotaAPIKeyIsUnsupported(t *testing.T) {
	srv := kimitest.New(t)
	access, _ := srv.Grant()
	acct := protocol.Account{ID: "kimi", Settings: srv.Settings(), Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: access}}
	_, err := readQuota(srv, acct)
	if e := asLLMError(t, err); e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential {
		t.Errorf("err = %+v, want unsupported with scope credential", e)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("an API key account made %d requests, want none", n)
	}
}

func TestQuotaErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*kimitest.Server, string)
		code  string
		scope string
	}{
		{"revoked token", func(s *kimitest.Server, a string) { s.Revoke(a) }, llm.CodeAuth, llm.ScopeCredential},
		{"rate limited", func(s *kimitest.Server, _ string) { s.UsageStatus = 429 }, llm.CodeRateLimit, llm.ScopeCredential},
		{"server error", func(s *kimitest.Server, _ string) { s.UsageStatus = 500 }, llm.CodeUpstream, llm.ScopeEndpoint},
		{"not found", func(s *kimitest.Server, _ string) { s.UsageStatus = 404 }, llm.CodeUpstream, llm.ScopeEndpoint},
		{"not JSON", func(s *kimitest.Server, _ string) { s.Usage = "<html>" }, llm.CodeUpstream, llm.ScopeEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := kimitest.New(t)
			access, _ := srv.Grant()
			tc.setup(srv, access)
			_, err := readQuota(srv, quotaAccount(srv, access))
			if e := asLLMError(t, err); e.Code != tc.code || e.Scope != tc.scope {
				t.Errorf("err = %+v, want %s with scope %s", e, tc.code, tc.scope)
			}
		})
	}

	t.Run("unreachable", func(t *testing.T) {
		srv := kimitest.New(t)
		access, _ := srv.Grant()
		acct := quotaAccount(srv, access)
		srv.Close()
		_, err := readQuota(srv, acct)
		if e := asLLMError(t, err); e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("err = %+v, want a retryable unavailable error", e)
		}
	})
}

// The plan is optional: a profile that fails or names no user leaves the limits and no plan.
func TestQuotaWithoutPlan(t *testing.T) {
	for name, set := range map[string]func(*kimitest.Server){
		"profile fails":    func(s *kimitest.Server) { s.MeStatus = 500 },
		"profile no user":  func(s *kimitest.Server) { s.Me = `{"user_level_name":"Andante"}` },
		"profile not JSON": func(s *kimitest.Server) { s.Me = `nope` },
	} {
		t.Run(name, func(t *testing.T) {
			srv := kimitest.New(t)
			access, _ := srv.Grant()
			set(srv)
			q, err := readQuota(srv, quotaAccount(srv, access))
			if err != nil {
				t.Fatal(err)
			}
			if q.Plan != "" || len(q.Limits) != 4 {
				t.Errorf("plan %q with %d limits, want no plan and 4 limits", q.Plan, len(q.Limits))
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(q.Raw, &raw); err != nil || raw["usage"] == nil {
				t.Errorf("raw = %s, want a JSON object with the usage reply", q.Raw)
			}
		})
	}
}

func TestKimiUsageLimits(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	var u kimiUsage
	if err := json.Unmarshal([]byte(`{
		"usage":{"name":"Plan quota","used":3,"limit":0},
		"limits":[
			{"window":{"duration":1,"timeUnit":"TIME_UNIT_DAY"},"name":"Chat","limit":10,"used":12,"reset_in":3600},
			{"detail":{"limit":"20","remaining":"5","ttl":90}},
			{"detail":{"name":"Review"},"duration":2,"timeUnit":"TIME_UNIT_WEEK"},
			{"window":{"duration":3,"timeUnit":"TIME_UNIT_HOUR"},"detail":{"limit":8,"used":0}},
			{"detail":{}}
		],
		"usages":{"limit_month_total":{"used_ratio":"1.5","reset_time":"bad"},"limit_month_code":{"reset_time":"2026-12-01T00:00:00Z"}}}`), &u); err != nil {
		t.Fatal(err)
	}
	got := u.limits(now)
	byID := map[string]protocol.QuotaLimit{}
	for _, l := range got {
		byID[l.ID] = l
	}
	if len(got) != len(byID) {
		t.Fatalf("limits repeat an id: %+v", got)
	}

	l := byID["limit_1"]
	if l.Label != "Chat" || *l.UsedPercent != 100 || *l.Remaining != 0 || l.WindowSeconds != 86400 || l.ResetsAt != now.Unix()+3600 {
		t.Errorf("an overused named limit: %+v", l)
	}
	l = byID["limit_2"]
	if l.Label != "Limit #2" || *l.Used != 15 || *l.UsedPercent != 75 || l.ResetsAt != now.Unix()+90 {
		t.Errorf("a limit with remaining only: %+v", l)
	}
	if _, ok := byID["limit_3"]; ok {
		t.Errorf("a limit with neither used nor limit was reported: %+v", byID["limit_3"])
	}
	l = byID["limit_4"]
	if l.Label != "3-hour limit" || *l.UsedPercent != 0 || l.WindowSeconds != 10800 {
		t.Errorf("an unnamed limit takes its window's label: %+v", l)
	}
	if _, ok := byID["limit_5"]; ok {
		t.Errorf("an empty limit was reported")
	}
	l = byID["usage"]
	if l.Label != "Plan quota" || *l.UsedPercent != 100 || l.Limit == nil || *l.Limit != 0 {
		t.Errorf("a summary with a zero limit and use: %+v", l)
	}
	l = byID["month_total"]
	if *l.UsedPercent != 100 || l.ResetsAt != 0 {
		t.Errorf("a ratio above 1 and a bad time: %+v", l)
	}
	if _, ok := byID["month_code"]; ok {
		t.Errorf("a monthly entry without a ratio was reported")
	}
}

func TestWindowLabel(t *testing.T) {
	for secs, want := range map[int64]string{
		5 * 3600: "5-hour limit", 86400: "Daily limit", 7 * 86400: "Weekly limit", 30 * 86400: "Monthly limit",
		3 * 86400: "3-day limit", 2 * 3600: "2-hour limit", 90 * 60: "90-minute limit", 45: "45-second limit",
	} {
		if got := windowLabel(secs); got != want {
			t.Errorf("windowLabel(%d) = %q, want %q", secs, got, want)
		}
	}
	// A missing or unknown unit counts as minutes.
	if got := windowSeconds(flexNum{300, true}, ""); got != 18000 {
		t.Errorf("300 with no unit = %d seconds, want 18000", got)
	}
}
