package main

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Codex quota is read from the ChatGPT backend's usage endpoint, {backend}/wham/usage, which the
// Codex client itself reads for its usage display. {backend} is the account's base URL without its
// trailing /codex (https://chatgpt.com/backend-api). The call spends nothing from the plan.
const (
	usagePath = "/wham/usage"
	// maxUsageBody bounds how much of the usage reply is read.
	maxUsageBody = 1 << 20
)

// quota reads a ChatGPT subscription's usage windows and credit balance (protocol minor version 5).
// An API key has no plan to report.
func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return nil, err
	}
	if !a.subscription {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "an API key has no ChatGPT plan to report", Scope: llm.ScopeCredential}
	}
	base := s.BaseURL
	if base == "" {
		base = defaultSubscriptionBaseURL
	}
	base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/codex")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+usagePath, nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)
	setIdentity(req.Header, nil, s, a)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := statusError(resp, "")
		if e.Code == llm.CodeNotFound {
			// The usage endpoint is missing; no model is involved.
			e.Scope = llm.ScopeEndpoint
		}
		return nil, e
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUsageBody))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading the usage reply: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	var u usagePayload
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "the usage reply is not valid JSON", Scope: llm.ScopeEndpoint, Status: resp.StatusCode}
	}
	q := &protocol.Quota{Plan: u.PlanType, Limits: u.limits(time.Now())}
	if q.Plan == "" {
		q.Plan = a.planType
	}
	q.Raw, _ = json.Marshal(map[string]json.RawMessage{"usage": body})
	return q, nil
}

// usagePayload is the usage endpoint's reply, as far as the extension reads it.
type usagePayload struct {
	PlanType            string      `json:"plan_type"`
	RateLimit           *rateLimit  `json:"rate_limit"`
	CodeReviewRateLimit *rateLimit  `json:"code_review_rate_limit"`
	Additional          []extraRate `json:"additional_rate_limits"`
	Credits             *struct {
		Balance   number `json:"balance"`
		Unlimited bool   `json:"unlimited"`
	} `json:"credits"`
}

type extraRate struct {
	LimitName      string     `json:"limit_name"`
	MeteredFeature string     `json:"metered_feature"`
	RateLimit      *rateLimit `json:"rate_limit"`
}

// rateLimit is a pair of usage windows: the primary one (5 hours on most plans) and the secondary
// (a week, or a month on some workspace plans).
type rateLimit struct {
	Allowed         *bool        `json:"allowed"`
	LimitReached    bool         `json:"limit_reached"`
	PrimaryWindow   *usageWindow `json:"primary_window"`
	SecondaryWindow *usageWindow `json:"secondary_window"`
}

type usageWindow struct {
	UsedPercent        number `json:"used_percent"`
	ResetAt            number `json:"reset_at"`
	ResetAfterSeconds  number `json:"reset_after_seconds"`
	LimitWindowSeconds number `json:"limit_window_seconds"`
}

// number is a JSON number or a string holding one, as the usage endpoint gives its amounts; ok is
// false for anything else.
type number struct {
	v  float64
	ok bool
}

func (n *number) UnmarshalJSON(b []byte) error {
	*n = number{}
	var s string
	if len(b) > 0 && b[0] == '"' {
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
	} else {
		s = string(b)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		*n = number{v, true}
	}
	return nil
}

const (
	fiveHourSeconds = 5 * 60 * 60
	weekSeconds     = 7 * 24 * 60 * 60
	minMonthSeconds = 28 * 24 * 60 * 60
	maxMonthSeconds = 31 * 24 * 60 * 60
)

func (w *usageWindow) seconds() (int64, bool) {
	if w == nil || !w.LimitWindowSeconds.ok {
		return 0, false
	}
	return int64(w.LimitWindowSeconds.v), true
}

func (w *usageWindow) monthly() bool {
	s, ok := w.seconds()
	return ok && s >= minMonthSeconds && s <= maxMonthSeconds
}

// classify tells the 5-hour window from the longer one by their lengths. A window with no length
// is taken by its place: the primary is the 5-hour window and the secondary the longer one.
func (r *rateLimit) classify() (five, long *usageWindow) {
	for _, w := range []*usageWindow{r.PrimaryWindow, r.SecondaryWindow} {
		s, _ := w.seconds()
		switch {
		case w == nil:
		case s == fiveHourSeconds && five == nil:
			five = w
		case (s == weekSeconds || w.monthly()) && long == nil:
			long = w
		}
	}
	if five == nil && r.PrimaryWindow != nil && r.PrimaryWindow != long {
		five = r.PrimaryWindow
	}
	if long == nil && r.SecondaryWindow != nil && r.SecondaryWindow != five {
		long = r.SecondaryWindow
	}
	return five, long
}

var notID = regexp.MustCompile(`[^a-z0-9]+`)

// limits turns the reply into the plan's limits, in the order the old panel shows them: the plan's
// own windows, code review's, each extra rate limit's (for example one model's), then the credit
// balance.
func (u *usagePayload) limits(now time.Time) []protocol.QuotaLimit {
	out := []protocol.QuotaLimit{}
	seen := map[string]bool{}
	// group adds the 5-hour and the longer window of r, under ids made from prefix.
	group := func(r *rateLimit, prefix string, labels [3]string) {
		if r == nil {
			return
		}
		reached := r.LimitReached || (r.Allowed != nil && !*r.Allowed)
		five, long := r.classify()
		longID, longLabel := "weekly", labels[1]
		if long.monthly() {
			longID, longLabel = "monthly", labels[2]
		}
		for _, c := range []struct {
			id, label string
			w         *usageWindow
		}{{"five_hour", labels[0], five}, {longID, longLabel, long}} {
			if c.w == nil {
				continue
			}
			id := prefix + c.id
			for n := 2; seen[id]; n++ {
				id = prefix + c.id + "_" + strconv.Itoa(n)
			}
			seen[id] = true
			out = append(out, c.w.limit(id, c.label, reached, now))
		}
	}
	group(u.RateLimit, "", [3]string{"5-hour limit", "Weekly limit", "Monthly limit"})
	group(u.CodeReviewRateLimit, "code_review_", [3]string{"Code review 5-hour limit", "Code review weekly limit", "Code review monthly limit"})
	for i, x := range u.Additional {
		name := strings.TrimSpace(x.LimitName)
		if name == "" {
			name = strings.TrimSpace(x.MeteredFeature)
		}
		prefix := strings.Trim(notID.ReplaceAllString(strings.ToLower(name), "_"), "_")
		if prefix == "" {
			prefix = "additional_" + strconv.Itoa(i+1)
		}
		if name == "" {
			name = "Additional"
		}
		group(x.RateLimit, prefix+"_", [3]string{name + " 5-hour limit", name + " weekly limit", name + " monthly limit"})
	}
	if c := u.Credits; c != nil {
		switch {
		case c.Unlimited:
			out = append(out, protocol.QuotaLimit{ID: "credits", Label: "Credit balance", Unlimited: true, Unit: "credits"})
		case c.Balance.ok && c.Balance.v >= 0:
			b := c.Balance.v
			out = append(out, protocol.QuotaLimit{ID: "credits", Label: "Credit balance", Remaining: &b, Unit: "credits"})
		}
	}
	return out
}

// limit builds the limit a window reports. A window that says nothing of its use but whose rate
// limit is reached, with a reset time, counts as fully used.
func (w *usageWindow) limit(id, label string, reached bool, now time.Time) protocol.QuotaLimit {
	l := protocol.QuotaLimit{ID: id, Label: label}
	if w.ResetAt.ok {
		l.ResetsAt = unixSeconds(w.ResetAt.v)
	} else if w.ResetAfterSeconds.ok && w.ResetAfterSeconds.v > 0 {
		l.ResetsAt = now.Unix() + int64(w.ResetAfterSeconds.v)
	}
	if s, ok := w.seconds(); ok && s > 0 {
		l.WindowSeconds = s
	}
	switch {
	case w.UsedPercent.ok:
		p := clampPercent(w.UsedPercent.v)
		l.UsedPercent = &p
	case reached && l.ResetsAt > 0:
		p := 100.0
		l.UsedPercent = &p
	}
	return l
}

// unixSeconds reads a Unix time given in seconds or milliseconds.
func unixSeconds(v float64) int64 {
	if v <= 0 {
		return 0
	}
	if v >= 1e11 {
		v /= 1000
	}
	return int64(v)
}

func clampPercent(v float64) float64 { return math.Min(100, math.Max(0, v)) }
