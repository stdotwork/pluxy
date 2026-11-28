package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	usagePath    = "/api/oauth/usage"
	profilePath  = "/api/oauth/profile"
	maxQuotaBody = 1 << 20
)

func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	setupToken := c != nil && c.Kind == protocol.CredentialAPIKey && isOAuthToken(c.APIKey)
	if c != nil && c.Kind == protocol.CredentialAPIKey && !setupToken {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "an API key has no subscription plan to report", Scope: llm.ScopeCredential}
	}
	usage, err := getJSON(ctx, call.Account.Credential, s.BaseURL+usagePath)
	if err != nil {
		return nil, err
	}
	var u map[string]json.RawMessage
	if json.Unmarshal(usage, &u) != nil || u == nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the usage reply is not a JSON object", Scope: llm.ScopeEndpoint}
	}
	q := &protocol.Quota{Limits: usageLimits(u)}
	raw := map[string]json.RawMessage{"usage": usage}
	// A setup token belongs to a subscription, so its usage is read, but not its profile: such a
	// token is made for inference and lacks the profile scope.
	if ctx.Err() == nil && !setupToken {
		if profile, err := getJSON(ctx, call.Account.Credential, s.BaseURL+profilePath); err == nil {
			raw["profile"] = profile
			q.Plan = planOf(profile)
		}
	}
	q.Raw, _ = json.Marshal(raw)
	return q, nil
}

func getJSON(ctx context.Context, cred *protocol.Credential, url string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Anthropic-Version", anthropicVersion)
	if err := authorize(req.Header, cred); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := upstreamError(resp, "", false)
		if e.Code == llm.CodeNotFound {
			e.Scope = llm.ScopeEndpoint
		}
		return nil, e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBody))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading the reply: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	if !json.Valid(body) {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the reply is not valid JSON", Scope: llm.ScopeEndpoint, Status: resp.StatusCode}
	}
	return body, nil
}

var usageWindows = []struct {
	key, id, label string
	family         string
	feature        bool
}{
	{"five_hour", "five_hour", "5-hour limit", "", false},
	{"seven_day", "seven_day", "Weekly limit", "", false},
	{"seven_day_oauth_apps", "seven_day_oauth_apps", "Weekly OAuth apps limit", "", true},
	{"seven_day_opus", "seven_day_opus", "Weekly Opus limit", "opus", false},
	{"seven_day_sonnet", "seven_day_sonnet", "Weekly Sonnet limit", "sonnet", false},
	{"seven_day_cowork", "seven_day_cowork", "Weekly Cowork limit", "", true},
	{"iguana_necktie", "seven_day_fable", "Weekly Fable limit", "fable", false},
}

func familyModels(family string) []string {
	var list protocol.ModelList
	if json.Unmarshal(modelsJSON, &list) != nil {
		return nil
	}
	var out []string
	for _, m := range list.Models {
		if strings.Contains(strings.ToLower(m.ID), family) {
			out = append(out, m.ID)
		}
	}
	return out
}

const (
	fiveHourSeconds = 5 * 60 * 60
	weekSeconds     = 7 * 24 * 60 * 60
)

type usageWindow struct {
	Utilization      number `json:"utilization"`
	ResetsAt         string `json:"resets_at"`
	LimitDollars     number `json:"limit_dollars"`
	UsedDollars      number `json:"used_dollars"`
	RemainingDollars number `json:"remaining_dollars"`
}

var dollarFields = []string{"limit_dollars", "used_dollars", "remaining_dollars", "utilization", "resets_at"}

func usageLimits(u map[string]json.RawMessage) []protocol.QuotaLimit {
	out := []protocol.QuotaLimit{}
	known := map[string]bool{"extra_usage": true}
	fable := fableLimit(u["limits"])
	for _, w := range usageWindows {
		known[w.key] = true
		win, dollars, ok := readWindow(u[w.key])
		if !ok {
			continue
		}
		id, label, family, feature := w.id, w.label, w.family, w.feature
		// Claude gives the length of none: the 5-hour window is named so, the others are weekly.
		secs := int64(weekSeconds)
		if w.key == "five_hour" {
			secs = fiveHourSeconds
		}
		if w.key == "iguana_necktie" {
			if dollars {
				id, label, secs, family, feature = "cloud_session_credits", "Cloud session credits", 0, "", true
			} else if fable != nil {
				continue
			}
		}
		l := win.limit(id, label, secs, dollars)
		l.Feature = feature
		if family != "" {
			l.Models = familyModels(family)
		}
		out = append(out, l)
	}
	if fable != nil {
		used := clampPercent(*fable.Percent)
		out = append(out, protocol.QuotaLimit{ID: "seven_day_fable", Label: "Weekly Fable limit", UsedPercent: &used,
			ResetsAt: parseTime(fable.ResetsAt), WindowSeconds: weekSeconds, Models: familyModels("fable")})
	}
	var keys []string
	for k := range u {
		if !known[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if win, dollars, ok := readWindow(u[k]); ok && dollars {
			l := win.limit(k, k, 0, true)
			l.Feature = true
			out = append(out, l)
		}
	}
	if l, ok := extraUsage(u["extra_usage"]); ok {
		out = append(out, l)
	}
	return out
}

func readWindow(raw json.RawMessage) (w usageWindow, dollars, ok bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || json.Unmarshal(raw, &w) != nil {
		return w, false, false
	}
	if _, has := fields["utilization"]; !has {
		_, has = fields["limit_dollars"]
		if !has {
			return w, false, false
		}
	}
	dollars = true
	for _, f := range dollarFields {
		if _, has := fields[f]; !has {
			dollars = false
		}
	}
	return w, dollars, true
}

func (w usageWindow) limit(id, label string, seconds int64, dollars bool) protocol.QuotaLimit {
	l := protocol.QuotaLimit{ID: id, Label: label, ResetsAt: parseTime(w.ResetsAt), WindowSeconds: seconds}
	if w.Utilization.ok {
		p := clampPercent(w.Utilization.v)
		l.UsedPercent = &p
	}
	if dollars {
		l.Unit = "usd"
		l.Limit, l.Used, l.Remaining = w.LimitDollars.ptr(), w.UsedDollars.ptr(), w.RemainingDollars.ptr()
	}
	return l
}

type scopedLimit struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	IsActive bool     `json:"is_active"`
	Scope    struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

func fableLimit(raw json.RawMessage) *scopedLimit {
	var list []scopedLimit
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	var found *scopedLimit
	for i := range list {
		l := &list[i]
		name := strings.ToLower(strings.TrimSpace(l.Scope.Model.DisplayName))
		if strings.ToLower(strings.TrimSpace(l.Kind)) != "weekly_scoped" || (name != "fable" && name != "fable 5") ||
			l.Percent == nil || math.IsNaN(*l.Percent) || math.IsInf(*l.Percent, 0) {
			continue
		}
		if l.IsActive {
			return l
		}
		if found == nil {
			found = l
		}
	}
	return found
}

func extraUsage(raw json.RawMessage) (protocol.QuotaLimit, bool) {
	var x struct {
		IsEnabled    *bool  `json:"is_enabled"`
		MonthlyLimit number `json:"monthly_limit"`
		UsedCredits  number `json:"used_credits"`
		Utilization  number `json:"utilization"`
		Currency     string `json:"currency"`
	}
	if json.Unmarshal(raw, &x) != nil || (x.IsEnabled != nil && !*x.IsEnabled) {
		return protocol.QuotaLimit{}, false
	}
	if !x.MonthlyLimit.ok && !x.UsedCredits.ok && !x.Utilization.ok {
		return protocol.QuotaLimit{}, false
	}
	l := protocol.QuotaLimit{ID: "extra_usage", Label: "Extra usage", Unit: "usd"}
	if c := strings.ToLower(strings.TrimSpace(x.Currency)); c != "" {
		l.Unit = c
	}
	if x.Utilization.ok {
		p := clampPercent(x.Utilization.v)
		l.UsedPercent = &p
	}
	major := func(n number) *float64 {
		if !n.ok {
			return nil
		}
		v := n.v / 100
		return &v
	}
	l.Limit, l.Used = major(x.MonthlyLimit), major(x.UsedCredits)
	if l.Limit != nil && l.Used != nil {
		r := *l.Limit - *l.Used
		l.Remaining = &r
	}
	return l, true
}

func planOf(profile json.RawMessage) string {
	var p struct {
		Account struct {
			HasClaudeMax flag `json:"has_claude_max"`
			HasClaudePro flag `json:"has_claude_pro"`
		} `json:"account"`
		Organization struct {
			OrganizationType   string `json:"organization_type"`
			SubscriptionStatus string `json:"subscription_status"`
			RateLimitTier      string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if json.Unmarshal(profile, &p) != nil {
		return ""
	}
	lower := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	o := p.Organization
	tier := lower(o.RateLimitTier)
	switch {
	case lower(o.OrganizationType) == "claude_team" && lower(o.SubscriptionStatus) == "active":
		return "Team"
	case strings.HasSuffix(tier, "claude_max_20x"):
		return "Max 20x"
	case strings.HasSuffix(tier, "claude_max_5x"):
		return "Max 5x"
	case p.Account.HasClaudeMax.on():
		return "Max"
	case p.Account.HasClaudePro.on():
		return "Pro"
	case p.Account.HasClaudeMax.off() && p.Account.HasClaudePro.off():
		return "Free"
	}
	return ""
}

type flag struct{ set, v bool }

func (f *flag) UnmarshalJSON(b []byte) error {
	*f = flag{}
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	switch x := v.(type) {
	case bool:
		*f = flag{true, x}
	case float64:
		*f = flag{true, x != 0}
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "y", "on":
			*f = flag{true, true}
		case "false", "0", "no", "n", "off":
			*f = flag{true, false}
		}
	}
	return nil
}

func (f flag) on() bool  { return f.set && f.v }
func (f flag) off() bool { return f.set && !f.v }

type number struct {
	v  float64
	ok bool
}

func (n *number) UnmarshalJSON(b []byte) error {
	*n = number{}
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		*n = number{v, true}
	}
	return nil
}

func (n number) ptr() *float64 {
	if !n.ok {
		return nil
	}
	return &n.v
}

func clampPercent(v float64) float64 { return math.Min(100, math.Max(0, v)) }

func parseTime(s string) int64 {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return t.Unix()
}
