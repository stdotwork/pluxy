package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// quota reads the account's usage from Cloud Code's retrieveUserQuotaSummary, and its plan from
// loadCodeAssist (the call the login makes to find the account's project), with the bearer token
// and the Antigravity User-Agent the account's requests carry, on the account's Cloud Code host
// (protocol minor version 5). Neither call spends from the plan. The plan is optional: a failed
// loadCodeAssist leaves it empty.
func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	if c != nil && c.Kind != protocol.CredentialOAuth {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only an Antigravity login has a plan to report", Scope: llm.ScopeCredential}
	}
	if c == nil || c.AccessToken == "" {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the account has no Antigravity login", Scope: llm.ScopeCredential}
	}
	data := readData(c)
	if data.ProjectID == "" {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the account's login names no Google Cloud project; log in again", Scope: llm.ScopeCredential}
	}
	h := http.Header{"User-Agent": {s.UserAgent}}

	status, body, err := cloudCode(ctx, s.BaseURL+"/v1internal:retrieveUserQuotaSummary", c.AccessToken, h, map[string]string{"project": data.ProjectID})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, quotaFailure(status, body)
	}
	var summary quotaSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Cloud Code's quota reply is not JSON", Scope: llm.ScopeEndpoint, Status: status}
	}
	q := &protocol.Quota{Limits: summary.limits()}
	raw := map[string]json.RawMessage{"usage": body}

	// The plan is the account's tier, as the Antigravity page shows it.
	switch status, profile, err := cloudCode(ctx, s.BaseURL+"/v1internal:loadCodeAssist", c.AccessToken, h, map[string]any{"metadata": map[string]any{"ideType": "ANTIGRAVITY"}}); {
	case err != nil && ctx.Err() != nil:
		return nil, ctx.Err()
	case err == nil && status == http.StatusOK:
		if plan, ok := planOf(profile); ok {
			q.Plan = plan
			raw["profile"] = profile
		}
	}
	q.Raw, _ = json.Marshal(raw)
	return q, nil
}

// quotaFailure turns a refusal of a quota read into an error as generate's failures are, except
// that what a rate limit or a missing page holds back is the account, not a model.
func quotaFailure(status int, body []byte) *llm.Error {
	var g googleError
	_ = json.Unmarshal(body, &g)
	e := classify(status, &g, body, "", nil)
	switch {
	case e.Code == llm.CodeNotFound:
		e.Code, e.Scope = llm.CodeUpstream, llm.ScopeEndpoint
	case e.Code == llm.CodeQuota:
		e.Code = llm.CodeRateLimit
	}
	if e.Scope == llm.ScopeModel {
		e.Scope = llm.ScopeCredential
	}
	return e
}

// planNames are the names of Cloud Code's tiers, as the Antigravity page shows them.
var planNames = map[string]string{
	"free-tier":          "Free",
	"g1-pro-tier":        "Pro",
	"g1-ultra-tier":      "Ultra",
	"g1-ultra-lite-tier": "Ultra Lite",
}

// planOf reads the account's plan from a loadCodeAssist reply: the paid tier when it names one,
// else the current tier, by its known name, else the name or ID Cloud Code gives it.
func planOf(body []byte) (string, bool) {
	var r struct {
		PaidTier    *struct{ ID, Name string } `json:"paidTier"`
		CurrentTier *struct{ ID, Name string } `json:"currentTier"`
	}
	if json.Unmarshal(body, &r) != nil {
		return "", false
	}
	t := r.CurrentTier
	if r.PaidTier != nil && strings.TrimSpace(r.PaidTier.ID) != "" {
		t = r.PaidTier
	}
	if t == nil {
		return "", false
	}
	id, name := strings.TrimSpace(t.ID), strings.TrimSpace(t.Name)
	if p, ok := planNames[id]; ok {
		return p, true
	}
	if name != "" {
		return name, true
	}
	return id, id != ""
}

// flexFraction is a share Cloud Code sends as a number, a numeric string or a percent string
// ("40%"). Anything else is not set.
type flexFraction struct {
	v  float64
	ok bool
}

func (f *flexFraction) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		s = string(b)
	}
	s = strings.TrimSpace(s)
	div := 1.0
	if t, ok := strings.CutSuffix(s, "%"); ok {
		s, div = t, 100
	}
	v, err := strconv.ParseFloat(s, 64)
	if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
		f.v, f.ok = v/div, true
	}
	return nil
}

// quotaSummary is retrieveUserQuotaSummary's reply: groups of models that share quota, each with
// buckets, which are the usage windows. Cloud Code spells fields in camel case; snake case is
// taken too.
type quotaSummary struct {
	Groups []struct {
		DisplayName  string `json:"displayName"`
		DisplayName2 string `json:"display_name"`
		Description  string `json:"description"`
		Buckets      []struct {
			BucketID          string       `json:"bucketId"`
			BucketID2         string       `json:"bucket_id"`
			DisplayName       string       `json:"displayName"`
			DisplayName2      string       `json:"display_name"`
			Description       string       `json:"description"`
			Window            string       `json:"window"`
			RemainingFraction flexFraction `json:"remainingFraction"`
			RemainingFrac2    flexFraction `json:"remaining_fraction"`
			ResetTime         string       `json:"resetTime"`
			ResetTime2        string       `json:"reset_time"`
		} `json:"buckets"`
	} `json:"groups"`
}

type bucketKind struct{ key, label string }

var (
	fiveHour = bucketKind{"five_hour", "5-hour limit"}
	daily    = bucketKind{"daily", "Daily limit"}
	weekly   = bucketKind{"weekly", "Weekly limit"}
	monthly  = bucketKind{"monthly", "Monthly limit"}

	// bucketByName and bucketByWindow name a bucket by the label Cloud Code gives it, or else by
	// its window, as the old panel does. Cloud Code's labels change between versions (for example
	// "Five Hour Limit Remaining"), so the label is tried first, then the structured window.
	bucketByName = map[string]bucketKind{
		"weekly limit": weekly, "daily limit": daily, "monthly limit": monthly,
		"5 hour limit": fiveHour, "5-hour limit": fiveHour, "five hour limit": fiveHour, "five-hour limit": fiveHour,
	}
	bucketByWindow = map[string]bucketKind{
		"5h": fiveHour, "five-hour": fiveHour, "five_hour": fiveHour,
		"daily": daily, "day": daily, "weekly": weekly, "week": weekly, "monthly": monthly, "month": monthly,
	}
	windowSecs = map[bucketKind]int64{fiveHour: 5 * 3600, daily: 86400, weekly: 7 * 86400}

	groupByName = map[string]string{"gemini models": "Gemini models", "claude and gpt models": "Claude and GPT models"}

	groupModels = regexp.MustCompile(`(?i)^models within this group:\s*(.+)$`)
	// remainingSuffix is the "remaining" Cloud Code adds to a label ("Weekly Limit Remaining").
	remainingSuffix = regexp.MustCompile(`\s*[-:]?\s*remaining$`)
	nonAlnum        = regexp.MustCompile(`[^a-z0-9]+`)
	spaces          = regexp.MustCompile(`\s+`)
)

func normalizeText(s string) string {
	return spaces.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), " ")
}

func slug(s string) string {
	return strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(s), "_"), "_")
}

// limits maps each bucket of each group to a limit. The share used is 100 times what is not
// remaining. A bucket that gives no remaining share is skipped.
func (qs *quotaSummary) limits() []protocol.QuotaLimit {
	var out []protocol.QuotaLimit
	seen := map[string]int{}
	for gi, g := range qs.Groups {
		group := firstNonEmpty(g.DisplayName, g.DisplayName2)
		if group == "" {
			group = "Quota Group " + strconv.Itoa(gi+1)
		}
		if canon, ok := groupByName[normalizeText(group)]; ok {
			group = canon
		}
		groupID := slug(group)
		if groupID == "" {
			groupID = "group" + strconv.Itoa(gi+1)
		}
		var models []string
		if m := groupModels.FindStringSubmatch(strings.TrimSpace(g.Description)); m != nil {
			for _, name := range strings.Split(m[1], ",") {
				if name = strings.TrimSpace(name); name != "" {
					models = append(models, name)
				}
			}
		}

		type item struct {
			limit protocol.QuotaLimit
			order int
			label string
		}
		var items []item
		for bi, b := range g.Buckets {
			frac := b.RemainingFraction
			if !frac.ok {
				frac = b.RemainingFrac2
			}
			if !frac.ok {
				continue
			}
			window := strings.TrimSpace(b.Window)
			bucketID := firstNonEmpty(b.BucketID, b.BucketID2)
			label := firstNonEmpty(b.DisplayName, b.DisplayName2, b.Description, window, bucketID)
			name := normalizeText(label)
			kind, known := bucketByName[name]
			if !known {
				kind, known = bucketByName[remainingSuffix.ReplaceAllString(name, "")]
			}
			if !known {
				kind, known = bucketByWindow[normalizeText(window)]
			}
			key, full := slug(window), label
			if key == "" {
				key = slug(bucketID)
			}
			if key == "" {
				key = "bucket" + strconv.Itoa(bi+1)
			}
			if known {
				key, label = kind.key, kind.label
				full = strings.ToLower(label[:1]) + label[1:]
			}
			used := math.Round(100*(1-math.Min(1, math.Max(0, frac.v)))*1e6) / 1e6
			l := protocol.QuotaLimit{ID: groupID + "_" + key, Label: group + " " + full, UsedPercent: &used, Models: models}
			if known {
				l.WindowSeconds = windowSecs[kind]
			}
			if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(firstNonEmpty(b.ResetTime, b.ResetTime2))); err == nil {
				l.ResetsAt = t.Unix()
			}
			order := 2
			switch {
			case known && kind == fiveHour:
				order = 0
			case known && kind == weekly:
				order = 1
			}
			items = append(items, item{l, order, label})
		}
		// The shortest window first, as the old panel lists them, then by label.
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].order != items[j].order {
				return items[i].order < items[j].order
			}
			return items[i].label < items[j].label
		})
		for _, it := range items {
			l := it.limit
			if n := seen[l.ID]; n > 0 {
				l.ID += "_" + strconv.Itoa(n+1)
			}
			seen[it.limit.ID]++
			out = append(out, l)
		}
	}
	return out
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
