package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A Muse Code login's quota is what Muse Code's key endpoint reports with the key it mints:
// POST https://api.meta.ai/muse-code/key with the device token (the credential's refresh token), as
// today's panel reads it. The device token renews the account, so it goes only where a renewal
// sends it: Meta's own key endpoint (mint_url only in a test run, see sdk.TestLoginHost), never to
// base_url, which an account may point at a relay. Meta has no other usage endpoint. The call makes a new API key; the reply's key is
// dropped here, never returned and never kept, and the credential's own key is left alone: if Meta
// retires it, the account's next request is refused and core renews the credential (a renewal makes
// a key from the device token the same way).

// quotaBodyLimit bounds the reply read.
const quotaBodyLimit = 1 << 20

func quota(ctx context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	c := call.Account.Credential
	if c != nil && c.Kind == protocol.CredentialAPIKey {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "a Meta API key has no plan to report; only a Muse Code login has", Scope: llm.ScopeCredential}
	}
	if c == nil || c.Kind != protocol.CredentialOAuth || !strings.HasPrefix(c.RefreshToken, "dca:") {
		return nil, &llm.Error{Code: llm.CodeAuth, Message: "the account has no Muse Code device token; log in again", Scope: llm.ScopeCredential}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.MintURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad key endpoint: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.RefreshToken)
	req.Header.Set("x-api-version", "1.0.0")
	clientIdentity(req.Header)

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "upstream unreachable: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, quotaError(resp.StatusCode, resp.Header)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, quotaBodyLimit))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading upstream response: " + err.Error(), Scope: llm.ScopeEndpoint, Retryable: true}
	}
	return quotaFrom(body, resp.StatusCode)
}

// quotaError maps a refused key-endpoint call by its status alone: the endpoint's error text can
// echo keys and personal data, and core cannot redact a key it does not know.
func quotaError(status int, h http.Header) *llm.Error {
	e := &llm.Error{Status: status}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Code, e.Scope, e.Message = llm.CodeAuth, llm.ScopeCredential, "Meta refused the device token; log in again"
	case status == http.StatusTooManyRequests:
		e.Code, e.Scope, e.Retryable, e.Message = llm.CodeRateLimit, llm.ScopeCredential, true, "Meta is rate limiting this account"
		e.RetryAfterMS = retryAfter(h.Get("Retry-After"), time.Now())
	case status == http.StatusServiceUnavailable || status == 529:
		e.Code, e.Scope, e.Message = llm.CodeUnavailable, llm.ScopeEndpoint, "Meta's key endpoint is unavailable"
		e.RetryAfterMS = retryAfter(h.Get("Retry-After"), time.Now())
	case status >= 500:
		e.Code, e.Scope, e.Message = llm.CodeUpstream, llm.ScopeEndpoint, "Meta's key endpoint failed"
	default:
		e.Code, e.Scope, e.Message = llm.CodeInvalidRequest, llm.ScopeRequest, "Meta's key endpoint refused the request"
	}
	return e
}

// metaWindow is one usage window of the key endpoint's subs_usage.
type metaWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	ResetsAt    *float64 `json:"resets_at"`
	DurationMin *float64 `json:"window_duration_mins"`
}

func (w metaWindow) limit(id, label string, withDuration bool) (protocol.QuotaLimit, bool) {
	l := protocol.QuotaLimit{ID: id, Label: label}
	if p := w.UsedPercent; p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) {
		v := math.Max(0, math.Min(100, *p))
		l.UsedPercent = &v
	}
	if r := w.ResetsAt; r != nil && *r > 0 && *r < math.MaxInt64 {
		l.ResetsAt = int64(*r)
	}
	if d := w.DurationMin; withDuration && d != nil && *d > 0 && *d < math.MaxInt32 {
		l.WindowSeconds = int64(*d * 60)
	}
	return l, l.UsedPercent != nil || l.ResetsAt != 0
}

// quotaFrom reads the key endpoint's reply. It keeps the plan and the windows and drops the rest,
// the minted key above all: the raw reply is rebuilt from the fields it keeps.
func quotaFrom(body []byte, status int) (*protocol.Quota, error) {
	var doc struct {
		Tier   string          `json:"subs_tier_name"`
		Usage  json.RawMessage `json:"subs_usage"`
		Active json.RawMessage `json:"is_subs_active"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, &llm.Error{Code: llm.CodeUpstream, Message: "Meta's key endpoint sent no JSON", Status: status, Scope: llm.ScopeEndpoint}
	}
	var usage struct {
		Tier   string      `json:"tier"`
		Window *metaWindow `json:"window"`
		Weekly *metaWindow `json:"weekly"`
	}
	_ = json.Unmarshal(doc.Usage, &usage)
	q := &protocol.Quota{Plan: strings.TrimSpace(doc.Tier), Limits: []protocol.QuotaLimit{}}
	if q.Plan == "" {
		q.Plan = strings.TrimSpace(usage.Tier)
	}
	if usage.Window != nil {
		if l, ok := usage.Window.limit("window", "Usage limit", true); ok {
			q.Limits = append(q.Limits, l)
		}
	}
	if usage.Weekly != nil {
		if l, ok := usage.Weekly.limit("weekly", "Weekly limit", false); ok {
			q.Limits = append(q.Limits, l)
		}
	}
	// The raw reply keeps the plan and the usage as Meta sent them; the minted key and the person's
	// details are not kept.
	raw := map[string]json.RawMessage{}
	if doc.Tier != "" {
		raw["subs_tier_name"], _ = json.Marshal(doc.Tier)
	}
	if json.Valid(doc.Usage) {
		raw["subs_usage"] = doc.Usage
	}
	if json.Valid(doc.Active) {
		raw["is_subs_active"] = doc.Active
	}
	q.Raw, _ = json.Marshal(raw)
	return q, nil
}
