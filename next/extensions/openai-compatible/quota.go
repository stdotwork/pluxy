package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	quotaOpenRouter = "openrouter"
	quotaDeepSeek   = "deepseek"
	quotaNone       = "none"

	maxQuotaBody = 1 << 20
)

func quotaVendor(s settings) (string, error) {
	switch q := strings.TrimSpace(s.Quota); q {
	case quotaOpenRouter, quotaDeepSeek, quotaNone:
		return q, nil
	case "":
		u, err := url.Parse(strings.TrimSpace(s.BaseURL))
		if err != nil || u.Host == "" {
			return "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url", Scope: llm.ScopeCredential}
		}
		switch strings.ToLower(u.Hostname()) {
		case "openrouter.ai":
			return quotaOpenRouter, nil
		case "api.deepseek.com":
			return quotaDeepSeek, nil
		default:
			return quotaNone, nil
		}
	default:
		return "", &llm.Error{
			Code:    llm.CodeInvalidRequest,
			Message: fmt.Sprintf("account setting quota must be %q, %q or %q", quotaOpenRouter, quotaDeepSeek, quotaNone),
			Scope:   llm.ScopeCredential,
		}
	}
}

func quotaURL(baseURL, vendor string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	switch vendor {
	case quotaOpenRouter:
		for _, suffix := range []string{"/api/v1", "/api", "/v1", "/v1beta"} {
			if strings.HasSuffix(base, suffix) {
				base = strings.TrimSuffix(base, suffix)
				break
			}
		}
		return base + "/api/v1/key"
	case quotaDeepSeek:
		for _, suffix := range []string{"/anthropic/v1", "/anthropic", "/beta/v1", "/beta", "/v1", "/v1beta"} {
			if strings.HasSuffix(base, suffix) {
				base = strings.TrimSuffix(base, suffix)
				break
			}
		}
		return base + "/user/balance"
	default:
		return ""
	}
}

func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, _, key, err := account(sdk.Call{ID: call.ID, Account: call.Account})
	if err != nil {
		return nil, err
	}
	vendor, err := quotaVendor(s)
	if err != nil {
		return nil, err
	}
	if vendor == quotaNone {
		return nil, &llm.Error{
			Code:    llm.CodeUnsupported,
			Message: "this account's upstream has no quota endpoint",
			Scope:   llm.ScopeCredential,
		}
	}
	endpoint := quotaURL(s.BaseURL, vendor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := upstreamError(resp, "")
		if e.Code == llm.CodeNotFound {
			e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
		}
		return nil, e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBody))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading upstream quota response: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	switch vendor {
	case quotaOpenRouter:
		return parseOpenRouterQuota(body)
	case quotaDeepSeek:
		return parseDeepSeekQuota(body)
	default:
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "this account's upstream has no quota endpoint", Scope: llm.ScopeCredential}
	}
}

type quotaNum struct {
	v  float64
	ok bool
}

func (n *quotaNum) UnmarshalJSON(b []byte) error {
	*n = quotaNum{}
	s := string(b)
	if len(b) > 0 && b[0] == '"' {
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		*n = quotaNum{f, true}
	}
	return nil
}

func clampPercent(p float64) float64 {
	return math.Min(100, math.Max(0, p))
}

func resetIntervalSeconds(interval string) int64 {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "daily":
		return 86400
	case "weekly":
		return 7 * 86400
	case "monthly":
		return 30 * 86400
	default:
		return 0
	}
}

func parseUnixTime(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	if u := t.Unix(); u > 0 {
		return u
	}
	return 0
}

func parseOpenRouterQuota(body []byte) (*protocol.Quota, error) {
	body = bytes.TrimSpace(body)
	var outer struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &outer) != nil || len(outer.Data) == 0 || string(outer.Data) == "null" {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "OpenRouter's key reply has no data object", Scope: llm.ScopeEndpoint}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(outer.Data, &fields) != nil || len(fields) == 0 {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "OpenRouter's key reply has no data object", Scope: llm.ScopeEndpoint}
	}
	type openRouterBudget struct {
		Scope         string   `json:"scope"`
		LimitUSD      quotaNum `json:"limit_usd"`
		SpendUSD      quotaNum `json:"spend_usd"`
		RemainingUSD  quotaNum `json:"remaining_usd"`
		ResetInterval string   `json:"reset_interval"`
		ResetsAt      string   `json:"resets_at"`
	}
	var d struct {
		Limit                  quotaNum          `json:"limit"`
		LimitRemaining         quotaNum          `json:"limit_remaining"`
		LimitReset             string            `json:"limit_reset"`
		EffectiveBudget        *openRouterBudget `json:"effective_budget"`
		Usage                  quotaNum          `json:"usage"`
		UsageDaily             quotaNum          `json:"usage_daily"`
		UsageWeekly            quotaNum          `json:"usage_weekly"`
		UsageMonthly           quotaNum          `json:"usage_monthly"`
		IsFreeTier             *bool             `json:"is_free_tier"`
		FreeModelDailyRequests *struct {
			Used      quotaNum `json:"used"`
			Limit     quotaNum `json:"limit"`
			Remaining quotaNum `json:"remaining"`
		} `json:"free_model_daily_requests"`
	}
	if err := json.Unmarshal(outer.Data, &d); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "OpenRouter's key reply is not valid JSON", Scope: llm.ScopeEndpoint}
	}

	q := &protocol.Quota{Limits: []protocol.QuotaLimit{}, Raw: json.RawMessage(body)}
	if d.IsFreeTier != nil {
		if *d.IsFreeTier {
			q.Plan = "free"
		} else {
			q.Plan = "paid"
		}
	}

	_, hasLimitKey := fields["limit"]
	if b := d.EffectiveBudget; b != nil && (b.LimitUSD.ok || b.RemainingUSD.ok || b.SpendUSD.ok) {
		label := "Budget limit"
		if strings.TrimSpace(b.Scope) == "key_limit" {
			label = "Key limit"
		}
		lim := protocol.QuotaLimit{
			ID:            "limit",
			Label:         label,
			Unit:          "usd",
			ResetsAt:      parseUnixTime(b.ResetsAt),
			WindowSeconds: resetIntervalSeconds(b.ResetInterval),
		}
		if b.LimitUSD.ok {
			v := b.LimitUSD.v
			lim.Limit = &v
		}
		if b.RemainingUSD.ok {
			r := b.RemainingUSD.v
			lim.Remaining = &r
		} else if b.LimitUSD.ok && b.SpendUSD.ok {
			r := math.Max(0, b.LimitUSD.v-b.SpendUSD.v)
			lim.Remaining = &r
		}
		if b.SpendUSD.ok {
			u := b.SpendUSD.v
			lim.Used = &u
		} else if b.LimitUSD.ok && b.RemainingUSD.ok {
			u := math.Max(0, b.LimitUSD.v-b.RemainingUSD.v)
			lim.Used = &u
		}
		switch {
		case b.LimitUSD.ok && b.LimitUSD.v > 0 && b.RemainingUSD.ok:
			p := clampPercent((b.LimitUSD.v - b.RemainingUSD.v) / b.LimitUSD.v * 100)
			lim.UsedPercent = &p
		case b.LimitUSD.ok && b.LimitUSD.v > 0 && lim.Used != nil:
			p := clampPercent(*lim.Used / b.LimitUSD.v * 100)
			lim.UsedPercent = &p
		case (b.RemainingUSD.ok && b.RemainingUSD.v <= 0) || (b.LimitUSD.ok && b.LimitUSD.v <= 0 && lim.Used != nil && *lim.Used > 0):
			p := 100.0
			lim.UsedPercent = &p
		}
		q.Limits = append(q.Limits, lim)
	} else if d.Limit.ok || d.LimitRemaining.ok || d.Usage.ok || hasLimitKey {
		windowed := strings.TrimSpace(d.LimitReset) != ""
		lim := protocol.QuotaLimit{
			ID:            "limit",
			Label:         "Key limit",
			Unit:          "usd",
			WindowSeconds: resetIntervalSeconds(d.LimitReset),
		}
		if d.Limit.ok {
			v := d.Limit.v
			lim.Limit = &v
			if d.LimitRemaining.ok {
				r := d.LimitRemaining.v
				lim.Remaining = &r
				u := math.Max(0, d.Limit.v-d.LimitRemaining.v)
				lim.Used = &u
			} else if !windowed && d.Usage.ok {
				u := d.Usage.v
				lim.Used = &u
				r := math.Max(0, d.Limit.v-d.Usage.v)
				lim.Remaining = &r
			}
			switch {
			case d.Limit.v > 0 && d.LimitRemaining.ok:
				p := clampPercent((d.Limit.v - d.LimitRemaining.v) / d.Limit.v * 100)
				lim.UsedPercent = &p
			case d.Limit.v > 0 && !windowed && d.Usage.ok:
				p := clampPercent(d.Usage.v / d.Limit.v * 100)
				lim.UsedPercent = &p
			case (d.LimitRemaining.ok && d.LimitRemaining.v <= 0) || (!windowed && d.Usage.ok && d.Usage.v > 0):
				p := 100.0
				lim.UsedPercent = &p
			}
		} else {
			lim.Unlimited = !d.LimitRemaining.ok
			if !windowed && d.Usage.ok {
				u := d.Usage.v
				lim.Used = &u
			}
			if d.LimitRemaining.ok {
				r := d.LimitRemaining.v
				lim.Remaining = &r
			}
		}
		q.Limits = append(q.Limits, lim)
	}

	for _, w := range []struct {
		id, label string
		secs      int64
		used      quotaNum
	}{
		{"usage_daily", "Daily usage", 86400, d.UsageDaily},
		{"usage_weekly", "Weekly usage", 7 * 86400, d.UsageWeekly},
		{"usage_monthly", "Monthly usage", 30 * 86400, d.UsageMonthly},
	} {
		if !w.used.ok {
			continue
		}
		u := w.used.v
		q.Limits = append(q.Limits, protocol.QuotaLimit{
			ID:            w.id,
			Label:         w.label,
			Used:          &u,
			Unit:          "usd",
			WindowSeconds: w.secs,
		})
	}

	if f := d.FreeModelDailyRequests; f != nil && (f.Used.ok || f.Limit.ok || f.Remaining.ok) {
		lim := protocol.QuotaLimit{
			ID:            "free_model_daily_requests",
			Label:         "Free-model daily requests",
			Unit:          "requests",
			WindowSeconds: 86400,
		}
		if f.Used.ok {
			u := f.Used.v
			lim.Used = &u
		} else if f.Limit.ok && f.Remaining.ok {
			u := math.Max(0, f.Limit.v-f.Remaining.v)
			lim.Used = &u
		}
		if f.Limit.ok {
			l := f.Limit.v
			lim.Limit = &l
		}
		if f.Remaining.ok {
			r := f.Remaining.v
			lim.Remaining = &r
		} else if f.Limit.ok && lim.Used != nil {
			r := math.Max(0, f.Limit.v-*lim.Used)
			lim.Remaining = &r
		}
		switch {
		case f.Limit.ok && f.Limit.v > 0 && lim.Used != nil:
			p := clampPercent(*lim.Used / f.Limit.v * 100)
			lim.UsedPercent = &p
		case lim.Used != nil && *lim.Used > 0:
			p := 100.0
			lim.UsedPercent = &p
		}
		q.Limits = append(q.Limits, lim)
	}

	return q, nil
}

func parseDeepSeekQuota(body []byte) (*protocol.Quota, error) {
	body = bytes.TrimSpace(body)
	var r struct {
		BalanceInfos []struct {
			Currency        string   `json:"currency"`
			TotalBalance    quotaNum `json:"total_balance"`
			GrantedBalance  quotaNum `json:"granted_balance"`
			ToppedUpBalance quotaNum `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if json.Unmarshal(body, &r) != nil || len(r.BalanceInfos) == 0 {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "DeepSeek's balance reply has no balance_infos", Scope: llm.ScopeEndpoint}
	}

	q := &protocol.Quota{Limits: make([]protocol.QuotaLimit, 0, len(r.BalanceInfos)), Raw: json.RawMessage(body)}

	seen := map[string]bool{}
	for _, info := range r.BalanceInfos {
		if !info.TotalBalance.ok {
			continue
		}
		curr := strings.ToUpper(strings.TrimSpace(info.Currency))
		unit := strings.ToLower(curr)
		id, label := "balance", "Balance"
		if curr != "" {
			id = "balance_" + unit
			label = "Balance (" + curr + ")"
		} else {
			unit = "credits"
		}
		baseID := id
		for n := 2; seen[id]; n++ {
			id = baseID + "_" + strconv.Itoa(n)
		}
		seen[id] = true
		rem := info.TotalBalance.v
		q.Limits = append(q.Limits, protocol.QuotaLimit{
			ID:        id,
			Label:     label,
			Remaining: &rem,
			Unit:      unit,
		})
	}
	if len(q.Limits) == 0 {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "DeepSeek's balance reply has no total_balance", Scope: llm.ScopeEndpoint}
	}
	return q, nil
}
