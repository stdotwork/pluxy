package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// A Grok login's quota is what the Grok CLI shows: two billing reads on the CLI's chat endpoint,
// with the CLI's identity (setCLIIdentity), as today's panel reads them.
//
//	GET {cli}/billing?format=credits   the weekly credit window
//	GET {cli}/billing                  the monthly credits, the pay-as-you-go cap and the prepaid balance
//
// The plan's name comes from two profile reads, GET {cli}/user?include=subscription and
// GET {cli}/settings; a failure of either only leaves the plan unnamed. Nothing is generated: today's
// panel also sent a one-token chat to tell paid accounts apart, which spends from the account, and
// this does not.

// quotaBodyLimit bounds each reply read.
const quotaBodyLimit = 1 << 20

// Limit IDs.
const (
	limitWeekly   = "weekly"
	limitMonthly  = "monthly_credits"
	limitOnDemand = "pay_as_you_go"
	limitPrepaid  = "prepaid"
)

// Monthly credit allowances (in cents) that name SuperGrok and SuperGrok Heavy when the profile reads
// name no plan, the ones today's panel recognises.
const (
	superGrokLimitCents      = 15_000
	superGrokHeavyLimitCents = 150_000
)

func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	tg, err := accountTarget(s, call.Account.Credential)
	if err != nil {
		return nil, err
	}
	if !tg.cli {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "an xAI API key has no plan to report; only a Grok login has", Scope: llm.ScopeCredential}
	}

	credits, creditsErr := quotaGet(ctx, tg, s, "/billing?format=credits")
	monthly, monthlyErr := quotaGet(ctx, tg, s, "/billing")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// A refused token fails the call, whichever read it was; otherwise one good read is enough.
	for _, e := range []*llm.Error{creditsErr, monthlyErr} {
		if e != nil && e.Code == llm.CodeAuth {
			return nil, e
		}
	}
	if creditsErr != nil && monthlyErr != nil {
		return nil, creditsErr
	}

	var primary, fallback *billing
	if creditsErr == nil {
		primary = parseBilling(credits)
	}
	if monthlyErr == nil {
		fallback = parseBilling(monthly)
	}
	q := buildQuota(primary, fallback)
	if q == nil {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "xAI reports no usage for this account", Scope: llm.ScopeCredential}
	}

	raw := map[string]json.RawMessage{}
	keep := func(name string, body []byte, e *llm.Error) {
		if e == nil && json.Valid(body) {
			raw[name] = body
		}
	}
	keep("credits", credits, creditsErr)
	keep("billing", monthly, monthlyErr)

	user, userErr := quotaGet(ctx, tg, s, "/user?include=subscription")
	settingsBody, settingsErr := quotaGet(ctx, tg, s, "/settings")
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	keep("user", user, userErr)
	keep("settings", settingsBody, settingsErr)
	q.Plan = planName(user, userErr, settingsBody, settingsErr)
	if q.Plan == "" {
		q.Plan = planFromAllowance(primary, fallback)
	}
	q.Raw, _ = json.Marshal(raw)
	return q, nil
}

// quotaGet reads one of the CLI endpoint's GET paths with the login's token and the CLI's identity.
func quotaGet(ctx context.Context, tg target, s settings, path string) ([]byte, *llm.Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tg.base+path, nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Authorization", "Bearer "+tg.token)
	req.Header.Set("Accept", "application/json")
	setCLIIdentity(req, s)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &llm.Error{Code: llm.CodeUnavailable, Message: ctx.Err().Error(), Scope: llm.ScopeEndpoint}
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, upstreamError(resp, "")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, quotaBodyLimit))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	return body, nil
}

// number is a JSON number that xAI may send as a string, or inside {"val": ...} (an amount in cents).
type number struct{ v *float64 }

func (n *number) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' {
		var o struct {
			Val json.RawMessage `json:"val"`
		}
		if json.Unmarshal(b, &o) != nil {
			return nil
		}
		b = bytes.TrimSpace(o.Val)
	}
	n.v = parseNumber(b)
	return nil
}

func parseNumber(b []byte) *float64 {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
	} else {
		s = string(b)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

// instant is a time xAI sends as an RFC 3339 string or as Unix seconds or milliseconds; 0 if neither.
type instant int64

func (t *instant) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if json.Unmarshal(b, &s) != nil {
			return nil
		}
		s = strings.TrimSpace(s)
		if at, err := time.Parse(time.RFC3339, s); err == nil {
			*t = instant(at.Unix())
			return nil
		}
		b = []byte(s)
	}
	if f := parseNumber(b); f != nil && *f > 0 {
		if *f >= 1e11 {
			*f /= 1000
		}
		*t = instant(int64(*f))
	}
	return nil
}

type billingPeriod struct {
	Type  string  `json:"type"`
	Start instant `json:"start"`
	End   instant `json:"end"`
}

// billingConfig is the config object of a billing reply. xAI's fields come in camelCase, and the
// old clients also read snake_case.
type billingConfig struct {
	CurrentPeriod       *billingPeriod  `json:"currentPeriod"`
	CurrentPeriodS      *billingPeriod  `json:"current_period"`
	CreditUsagePercent  number          `json:"creditUsagePercent"`
	CreditUsagePercentS number          `json:"credit_usage_percent"`
	ProductUsage        json.RawMessage `json:"productUsage"`
	ProductUsageS       json.RawMessage `json:"product_usage"`
	MonthlyLimit        number          `json:"monthlyLimit"`
	MonthlyLimitS       number          `json:"monthly_limit"`
	Used                number          `json:"used"`
	OnDemandCap         number          `json:"onDemandCap"`
	OnDemandCapS        number          `json:"on_demand_cap"`
	OnDemandUsed        number          `json:"onDemandUsed"`
	OnDemandUsedS       number          `json:"on_demand_used"`
	PrepaidBalance      number          `json:"prepaidBalance"`
	PrepaidBalanceS     number          `json:"prepaid_balance"`
	BillingStart        instant         `json:"billingPeriodStart"`
	BillingStartS       instant         `json:"billing_period_start"`
	BillingEnd          instant         `json:"billingPeriodEnd"`
	BillingEndS         instant         `json:"billing_period_end"`
}

// billing is one billing reply, read. Amounts are cents, shares are percents, instants Unix seconds.
type billing struct {
	kind                      string // "weekly" or "monthly": which clock the reply's period is
	usagePercent              *float64
	periodStart, periodEnd    int64
	hasProducts               bool
	monthlyLimit, used        *float64
	onDemandCap, onDemandUsed *float64
	prepaid                   *float64
	billingStart, billingEnd  int64
	hasWeekly, hasMonthly     bool
}

func firstNumber(a, b number) *float64 {
	if a.v != nil {
		return a.v
	}
	return b.v
}

func firstInstant(a, b instant) int64 {
	if a != 0 {
		return int64(a)
	}
	return int64(b)
}

// parseBilling reads a billing reply, or nil when it holds nothing a limit can be made of.
func parseBilling(body []byte) *billing {
	var doc struct {
		Config *billingConfig `json:"config"`
	}
	if json.Unmarshal(body, &doc) != nil || doc.Config == nil {
		return nil
	}
	c := doc.Config
	b := &billing{}
	period := c.CurrentPeriod
	if period == nil {
		period = c.CurrentPeriodS
	}
	periodType := ""
	if period != nil {
		t := strings.ToLower(period.Type)
		switch {
		case strings.Contains(t, "weekly"):
			periodType = "weekly"
		case strings.Contains(t, "monthly"):
			periodType = "monthly"
		}
	}
	b.usagePercent = firstNumber(c.CreditUsagePercent, c.CreditUsagePercentS)
	b.billingStart = firstInstant(c.BillingStart, c.BillingStartS)
	b.billingEnd = firstInstant(c.BillingEnd, c.BillingEndS)
	b.periodStart, b.periodEnd = b.billingStart, b.billingEnd
	if period != nil {
		if period.Start != 0 {
			b.periodStart = int64(period.Start)
		}
		if period.End != 0 {
			b.periodEnd = int64(period.End)
		}
	}
	for _, raw := range []json.RawMessage{c.ProductUsage, c.ProductUsageS} {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) == nil && len(items) > 0 {
			b.hasProducts = true
		}
	}
	b.monthlyLimit = firstNumber(c.MonthlyLimit, c.MonthlyLimitS)
	b.used = c.Used.v
	b.onDemandCap = firstNumber(c.OnDemandCap, c.OnDemandCapS)
	b.onDemandUsed = firstNumber(c.OnDemandUsed, c.OnDemandUsedS)
	if b.onDemandUsed == nil && b.used != nil && b.monthlyLimit != nil {
		over := math.Max(0, *b.used-*b.monthlyLimit)
		b.onDemandUsed = &over
	}
	b.prepaid = firstNumber(c.PrepaidBalance, c.PrepaidBalanceS)

	b.hasWeekly = b.usagePercent != nil || periodType == "weekly" || b.hasProducts
	b.hasMonthly = b.monthlyLimit != nil || b.used != nil || (!b.hasWeekly && (b.onDemandCap != nil || b.billingEnd != 0))
	if !b.hasWeekly && !b.hasMonthly {
		return nil
	}
	switch {
	case !b.hasWeekly:
		b.kind = "monthly"
	case periodType == "":
		b.kind = "weekly"
	default:
		b.kind = periodType
	}
	if !b.hasMonthly {
		b.billingStart, b.billingEnd = 0, 0
	}
	return b
}

func pick(a, b *billing, f func(*billing) *float64) *float64 {
	for _, x := range []*billing{a, b} {
		if x != nil {
			if v := f(x); v != nil {
				return v
			}
		}
	}
	return nil
}

func pickInstant(a, b *billing, f func(*billing) int64) int64 {
	for _, x := range []*billing{a, b} {
		if x != nil {
			if v := f(x); v != 0 {
				return v
			}
		}
	}
	return 0
}

func clampPercent(p float64) float64 { return math.Max(0, math.Min(100, p)) }

func usd(cents float64) *float64 {
	v := cents / 100
	return &v
}

// buildQuota makes the limits out of the credits reply (primary) and the billing reply (fallback),
// either of which may be missing, or nil when neither holds anything.
func buildQuota(primary, fallback *billing) *protocol.Quota {
	if primary == nil && fallback == nil {
		return nil
	}
	q := &protocol.Quota{Limits: []protocol.QuotaLimit{}}

	// The reply that names the active period decides whether there is a weekly window; its dates are
	// the window's, never the other reply's, which run on the billing month's clock.
	source := primary
	if source == nil {
		source = fallback
	}
	weekly := source.kind == "weekly" && (source.usagePercent != nil || source.periodEnd != 0 || source.hasProducts)
	if weekly {
		l := protocol.QuotaLimit{ID: limitWeekly, Label: "Weekly limit", ResetsAt: source.periodEnd}
		if source.usagePercent != nil {
			p := clampPercent(*source.usagePercent)
			l.UsedPercent = &p
		}
		if source.periodStart > 0 && source.periodEnd > source.periodStart {
			l.WindowSeconds = source.periodEnd - source.periodStart
		}
		q.Limits = append(q.Limits, l)
	}

	monthlyLimit := pick(primary, fallback, func(b *billing) *float64 { return b.monthlyLimit })
	used := pick(primary, fallback, func(b *billing) *float64 { return b.used })
	billingStart := pickInstant(primary, fallback, func(b *billing) int64 { return b.billingStart })
	billingEnd := pickInstant(primary, fallback, func(b *billing) int64 { return b.billingEnd })
	emptyMonth := monthlyLimit != nil && *monthlyLimit == 0 && used != nil && *used == 0
	if (monthlyLimit != nil || used != nil || billingEnd != 0) && !(weekly && emptyMonth) {
		l := protocol.QuotaLimit{ID: limitMonthly, Label: "Monthly credits", Unit: "usd", ResetsAt: billingEnd}
		if billingStart > 0 && billingEnd > billingStart {
			l.WindowSeconds = billingEnd - billingStart
		}
		if used != nil {
			included := *used
			if monthlyLimit != nil && *monthlyLimit > 0 {
				included = math.Min(included, *monthlyLimit)
			}
			l.Used = usd(included)
			if monthlyLimit != nil && *monthlyLimit > 0 {
				p := clampPercent(included / *monthlyLimit * 100)
				l.UsedPercent = &p
				l.Remaining = usd(*monthlyLimit - included)
			}
		}
		if monthlyLimit != nil {
			l.Limit = usd(*monthlyLimit)
		}
		q.Limits = append(q.Limits, l)
	}

	if cap := pick(primary, fallback, func(b *billing) *float64 { return b.onDemandCap }); cap != nil && *cap > 0 {
		l := protocol.QuotaLimit{ID: limitOnDemand, Label: "Pay as you go", Unit: "usd", Limit: usd(*cap)}
		if od := pick(primary, fallback, func(b *billing) *float64 { return b.onDemandUsed }); od != nil {
			l.Used = usd(*od)
			l.Remaining = usd(math.Max(0, *cap-*od))
			p := clampPercent(*od / *cap * 100)
			l.UsedPercent = &p
		}
		q.Limits = append(q.Limits, l)
	}

	if bal := pick(primary, fallback, func(b *billing) *float64 { return b.prepaid }); bal != nil && *bal > 0 {
		q.Limits = append(q.Limits, protocol.QuotaLimit{ID: limitPrepaid, Label: "Prepaid", Unit: "usd", Remaining: usd(*bal)})
	}
	return q
}

// planName reads the plan from the profile reads: the settings' display name, else the user's tier.
func planName(user []byte, userErr *llm.Error, settings []byte, settingsErr *llm.Error) string {
	field := func(body []byte, err *llm.Error, keys ...string) string {
		if err != nil {
			return ""
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(body, &m) != nil {
			return ""
		}
		for _, k := range keys {
			var s string
			if json.Unmarshal(m[k], &s) == nil && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	if d := field(settings, settingsErr, "subscription_tier_display", "subscriptionTierDisplay"); d != "" {
		return d
	}
	return field(user, userErr, "subscriptionTier", "subscription_tier")
}

// planFromAllowance names the plan by its monthly credit allowance when the profile reads name none.
func planFromAllowance(primary, fallback *billing) string {
	limit := pick(primary, fallback, func(b *billing) *float64 { return b.monthlyLimit })
	switch {
	case limit == nil:
	case *limit == superGrokLimitCents:
		return "SuperGrok"
	case *limit == superGrokHeavyLimitCents:
		return "SuperGrok Heavy"
	}
	return ""
}
