package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/devin/devintest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func quotaAccount(srv *devintest.Server, tok string) protocol.Account {
	return protocol.Account{ID: "devin", Settings: srv.Settings(), Credential: tokenCredential(tok)}
}

func readQuota(acct protocol.Account) (*protocol.Quota, error) {
	return quota(context.Background(), sdk.QuotaCall{ID: "q", Account: acct})
}

func pf(v float64) *float64 { return &v }

func llmErr(t *testing.T, err error) *llm.Error {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an *llm.Error", err)
	}
	return e
}

func TestQuotaReadsUserStatus(t *testing.T) {
	srv := devintest.New(t)
	tok := srv.Token()
	q, err := readQuota(quotaAccount(srv, tok))
	if err != nil {
		t.Fatal(err)
	}

	reqs := srv.StatusRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d GetUserStatus requests, want 1", len(reqs))
	}
	h := reqs[0].Header
	if h.Get("Authorization") != "Basic "+tok+"-"+tok || h.Get("Connect-Protocol-Version") != "1" ||
		h.Get("Content-Type") != "application/proto" || h.Get("User-Agent") != "" {
		t.Errorf("headers = %v, want the CLI's: the token twice as Basic credentials, Connect 1, protobuf, no User-Agent", h)
	}
	top, err := parseFields(reqs[0].Message)
	if err != nil || len(top) != 1 || top[0].num != 1 {
		t.Fatalf("request message %x (%v), want one field 1, the metadata", reqs[0].Message, err)
	}
	meta, _ := parseFields(top[0].data)
	got := map[int]string{}
	for _, f := range meta {
		got[f.num] = string(f.data)
	}
	if len(got[31]) != fingerprintLen {
		t.Errorf("fingerprint has %d characters, want %d", len(got[31]), fingerprintLen)
	}
	want := map[int]string{1: cliName, 2: cliVersion, 3: tok, 4: "en", 5: runtime.GOOS, 7: cliVersion, 12: cliName, 31: got[31]}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("metadata = %v, want %v", got, want)
	}

	if q.Plan != devintest.PlanName {
		t.Errorf("plan = %q, want %q", q.Plan, devintest.PlanName)
	}
	wantLimits := []protocol.QuotaLimit{
		{ID: "daily", Label: "Daily limit", UsedPercent: pf(100 - devintest.DailyRemaining), ResetsAt: devintest.DailyResetAtUnix, WindowSeconds: 86400},
		{ID: "weekly", Label: "Weekly limit", UsedPercent: pf(100 - devintest.WeeklyRemaining), ResetsAt: devintest.WeeklyResetAtUnix, WindowSeconds: 604800},
	}
	if !reflect.DeepEqual(q.Limits, wantLimits) {
		g, _ := json.Marshal(q.Limits)
		w, _ := json.Marshal(wantLimits)
		t.Errorf("limits:\n got %s\nwant %s", g, w)
	}
	var raw struct {
		UserStatus struct {
			PlanStatus map[string]any `json:"plan_status"`
		} `json:"user_status"`
	}
	if err := json.Unmarshal(q.Raw, &raw); err != nil || raw.UserStatus.PlanStatus["plan_name"] != devintest.PlanName || raw.UserStatus.PlanStatus["plan_end"] != float64(devintest.PlanEnd) {
		t.Errorf("raw = %s (%v), want the plan name and end", q.Raw, err)
	}
	for _, private := range []string{devintest.UserName, "user@example.com", devintest.UserID, tok} {
		if strings.Contains(string(q.Raw), private) {
			t.Errorf("raw %s holds %q", q.Raw, private)
		}
	}
}

// The device seed fixes the fingerprint the quota read sends, as it does for chat requests.
func TestQuotaFingerprintFollowsSeed(t *testing.T) {
	srv := devintest.New(t)
	tok := srv.Token()
	acct := quotaAccount(srv, tok)
	acct.Settings, _ = json.Marshal(map[string]string{"base_url": srv.URL, "device_seed": "seed-1"})
	for range 2 {
		if _, err := readQuota(acct); err != nil {
			t.Fatal(err)
		}
	}
	reqs := srv.StatusRequests()
	if len(reqs) != 2 || string(reqs[0].Message) != string(reqs[1].Message) {
		t.Errorf("with a device seed the two requests differ or are missing: %d requests", len(reqs))
	}
}

// A window Devin gives a reset time but no percent for reports the reset only; one it says nothing
// of is left out.
func TestQuotaPartialStatus(t *testing.T) {
	srv := devintest.New(t)
	plan := devintest.Bytes(nil, 1, devintest.Bytes(nil, 2, []byte("Free")))
	plan = devintest.Varint(plan, 15, 100)
	plan = devintest.Varint(plan, 17, 1794268800)
	srv.UserStatus = devintest.Bytes(nil, 1, devintest.Bytes(nil, 13, plan))
	q, err := readQuota(quotaAccount(srv, srv.Token()))
	if err != nil {
		t.Fatal(err)
	}
	want := []protocol.QuotaLimit{
		{ID: "daily", Label: "Daily limit", ResetsAt: 1794268800, WindowSeconds: 86400},
		{ID: "weekly", Label: "Weekly limit", UsedPercent: pf(0), WindowSeconds: 604800},
	}
	if q.Plan != "Free" || !reflect.DeepEqual(q.Limits, want) {
		t.Errorf("plan %q, limits %+v; want Free and %+v", q.Plan, q.Limits, want)
	}

	srv.UserStatus = devintest.Bytes(nil, 1, devintest.Bytes(nil, 3, []byte("someone")))
	q, err = readQuota(quotaAccount(srv, srv.Token()))
	if err != nil || q.Plan != "" || len(q.Limits) != 0 {
		t.Errorf("a status without a plan: %+v, %v; want no plan and no limits", q, err)
	}
}

func TestQuotaErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*devintest.Server, string)
		code  string
		scope string
	}{
		{"revoked token", func(s *devintest.Server, tok string) { s.Revoke(tok) }, llm.CodeAuth, llm.ScopeCredential},
		{"forbidden", func(s *devintest.Server, _ string) { s.StatusCode = 403 }, llm.CodePermission, llm.ScopeCredential},
		{"rate limited", func(s *devintest.Server, _ string) { s.StatusCode = 429 }, llm.CodeRateLimit, llm.ScopeCredential},
		{"server error", func(s *devintest.Server, _ string) { s.StatusCode = 500 }, llm.CodeUpstream, llm.ScopeEndpoint},
		{"not found", func(s *devintest.Server, _ string) { s.StatusCode = 404 }, llm.CodeUpstream, llm.ScopeEndpoint},
		{"not protobuf", func(s *devintest.Server, _ string) { s.UserStatus = []byte{0xff, 0xff} }, llm.CodeUpstream, llm.ScopeEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := devintest.New(t)
			tok := srv.Token()
			tc.setup(srv, tok)
			_, err := readQuota(quotaAccount(srv, tok))
			if e := llmErr(t, err); e.Code != tc.code || e.Scope != tc.scope {
				t.Errorf("err = %+v, want %s with scope %s", e, tc.code, tc.scope)
			}
		})
	}

	t.Run("no token", func(t *testing.T) {
		srv := devintest.New(t)
		_, err := readQuota(protocol.Account{ID: "devin", Settings: srv.Settings()})
		if e := llmErr(t, err); e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential {
			t.Errorf("err = %+v, want auth with scope credential", e)
		}
		if n := len(srv.StatusRequests()); n != 0 {
			t.Errorf("%d requests for an account with no token, want none", n)
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		srv := devintest.New(t)
		acct := quotaAccount(srv, srv.Token())
		srv.Close()
		_, err := readQuota(acct)
		if e := llmErr(t, err); e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("err = %+v, want a retryable unavailable error", e)
		}
	})
}

// A login reports the account's user ID, not its name, as the identity.
func TestLoginGrantCarriesIdentity(t *testing.T) {
	srv := devintest.New(t)
	tok := srv.Token()
	set, _ := readSettings(srv.Settings())
	g, err := grant(context.Background(), set, tok, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]string{"user_id": devintest.UserID}); !reflect.DeepEqual(g.Identity, want) {
		t.Errorf("identity = %v, want %v", g.Identity, want)
	}
	if g.Label != devintest.UserName {
		t.Errorf("label = %q, want the user name", g.Label)
	}
}
