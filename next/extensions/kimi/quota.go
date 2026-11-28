package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	token, data, err := bearer(c)
	if err != nil {
		return nil, err
	}
	if c.Kind != protocol.CredentialOAuth {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only a Kimi Code login has a plan to report, not an API key", Scope: llm.ScopeCredential}
	}
	body, err := kimiGet(ctx, s.BaseURL+"/v1/usages", token, data.DeviceID)
	if err != nil {
		return nil, err
	}
	var u kimiUsage
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Kimi's usage reply is not JSON", Scope: llm.ScopeEndpoint}
	}

	q := &protocol.Quota{Limits: u.limits(time.Now())}
	raw := map[string]json.RawMessage{"usage": body}
	switch me, err := kimiGet(ctx, s.BaseURL+"/v1/me", token, data.DeviceID); {
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case err == nil:
		var p struct {
			UserID        string `json:"user_id"`
			UserLevelName string `json:"user_level_name"`
		}
		if json.Unmarshal(me, &p) == nil {
			raw["profile"] = me
			if strings.TrimSpace(p.UserID) != "" {
				q.Plan = strings.TrimSpace(p.UserLevelName)
			}
		}
	}
	q.Raw, _ = json.Marshal(raw)
	return q, nil
}

func kimiGet(ctx context.Context, url, token, deviceID string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "pluxy-kimi/"+version)
	deviceHeaders(req.Header, deviceID)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "Kimi is unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e := upstreamError(resp, "")
		if e.Code == llm.CodeNotFound {
			e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
		}
		return nil, e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "Kimi's reply broke off", Scope: llm.ScopeEndpoint, Retryable: true}
	}
	return bytes.TrimSpace(body), nil
}

// flexNum is a number Kimi sends as a JSON number or as a string (int64 values are strings in
// protobuf JSON). Anything else, or a missing value, is not set.
type flexNum struct {
	v  float64
	ok bool
}

func (n *flexNum) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		s = string(b)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		n.v, n.ok = f, true
	}
	return nil
}

type kimiDetail struct {
	Name      string  `json:"name"`
	Title     string  `json:"title"`
	Scope     string  `json:"scope"`
	Limit     flexNum `json:"limit"`
	Used      flexNum `json:"used"`
	Remaining flexNum `json:"remaining"`
	Duration  flexNum `json:"duration"`
	TimeUnit  string  `json:"timeUnit"`
	ResetAt   string  `json:"reset_at"`
	ResetAt2  string  `json:"resetAt"`
	ResetTime string  `json:"reset_time"`
	ResetTim2 string  `json:"resetTime"`
	ResetIn   flexNum `json:"reset_in"`
	ResetIn2  flexNum `json:"resetIn"`
	TTL       flexNum `json:"ttl"`
}

type kimiLimit struct {
	kimiDetail
	Detail *kimiDetail `json:"detail"`
	Window *kimiDetail `json:"window"`
}

type kimiUsage struct {
	Usage  *kimiDetail `json:"usage"`
	Limits []kimiLimit `json:"limits"`
	Usages map[string]struct {
		UsedRatio flexNum `json:"used_ratio"`
		ResetTime string  `json:"reset_time"`
	} `json:"usages"`
}

func (u *kimiUsage) limits(now time.Time) []protocol.QuotaLimit {
	var out []protocol.QuotaLimit
	for i, l := range u.Limits {
		d := &l.kimiDetail
		if l.Detail != nil {
			d = l.Detail
		}
		duration, unit := l.Duration, l.TimeUnit
		if l.Window != nil {
			duration, unit = l.Window.Duration, l.Window.TimeUnit
		}
		if !duration.ok {
			duration, unit = d.Duration, d.TimeUnit
		}
		window := windowSeconds(duration, unit)
		label := firstNonEmpty(l.Name, l.Title, l.Scope, d.Name, d.Title, d.Scope)
		if label == "" && window > 0 {
			label = windowLabel(window)
		}
		if label == "" {
			label = fmt.Sprintf("Limit #%d", i+1)
		}
		if lim, ok := d.limit(fmt.Sprintf("limit_%d", i+1), label, now); ok {
			lim.WindowSeconds = window
			out = append(out, lim)
		}
	}
	if u.Usage != nil {
		if lim, ok := u.Usage.limit("usage", firstNonEmpty(u.Usage.Name, u.Usage.Title, "Weekly limit"), now); ok {
			out = append(out, lim)
		}
	}
	for _, m := range []struct{ key, id, label string }{
		{"limit_month_total", "month_total", "Monthly limit"},
		{"limit_month_code", "month_code", "Monthly coding limit"},
	} {
		r, ok := u.Usages[m.key]
		if !ok || !r.UsedRatio.ok {
			continue
		}
		used := clampPercent(r.UsedRatio.v * 100)
		out = append(out, protocol.QuotaLimit{ID: m.id, Label: m.label, UsedPercent: &used, ResetsAt: unixOf(r.ResetTime)})
	}
	return out
}

func (d *kimiDetail) limit(id, label string, now time.Time) (protocol.QuotaLimit, bool) {
	used, limit := d.Used, d.Limit
	if !used.ok && limit.ok && d.Remaining.ok {
		used = flexNum{limit.v - d.Remaining.v, true}
	}
	if !used.ok && !limit.ok {
		return protocol.QuotaLimit{}, false
	}
	l := protocol.QuotaLimit{ID: id, Label: label}
	if !used.ok {
		used = flexNum{0, true}
	}
	l.Used = &used.v
	if limit.ok {
		l.Limit = &limit.v
		rem := math.Max(0, limit.v-used.v)
		l.Remaining = &rem
	}
	switch {
	case limit.ok && limit.v > 0:
		p := clampPercent(used.v / limit.v * 100)
		l.UsedPercent = &p
	case used.v > 0:
		p := 100.0
		l.UsedPercent = &p
	}
	l.ResetsAt = unixOf(firstNonEmpty(d.ResetAt, d.ResetAt2, d.ResetTime, d.ResetTim2))
	if l.ResetsAt == 0 {
		for _, in := range []flexNum{d.ResetIn, d.ResetIn2, d.TTL} {
			if in.ok && in.v > 0 {
				l.ResetsAt = now.Unix() + int64(in.v)
				break
			}
		}
	}
	return l, true
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

func clampPercent(p float64) float64 { return math.Min(100, math.Max(0, p)) }

func unixOf(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return t.Unix()
}

func windowSeconds(duration flexNum, unit string) int64 {
	if !duration.ok || duration.v <= 0 {
		return 0
	}
	unit = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(unit)), "TIME_UNIT_")
	per := 60.0
	switch strings.TrimSuffix(unit, "S") {
	case "SECOND":
		per = 1
	case "HOUR":
		per = 3600
	case "DAY":
		per = 86400
	case "WEEK":
		per = 604800
	}
	return int64(duration.v * per)
}

func windowLabel(seconds int64) string {
	const minute, hour, day = 60, 3600, 86400
	switch {
	case seconds == 5*hour:
		return "5-hour limit"
	case seconds == day:
		return "Daily limit"
	case seconds == 7*day:
		return "Weekly limit"
	case seconds >= 28*day && seconds <= 31*day:
		return "Monthly limit"
	case seconds%day == 0:
		return fmt.Sprintf("%d-day limit", seconds/day)
	case seconds%hour == 0:
		return fmt.Sprintf("%d-hour limit", seconds/hour)
	case seconds%minute == 0:
		return fmt.Sprintf("%d-minute limit", seconds/minute)
	}
	return fmt.Sprintf("%d-second limit", seconds)
}
