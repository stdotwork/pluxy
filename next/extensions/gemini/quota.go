package main

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	typeErrorInfo    = "type.googleapis.com/google.rpc.ErrorInfo"
	typeRetryInfo    = "type.googleapis.com/google.rpc.RetryInfo"
	typeQuotaFailure = "type.googleapis.com/google.rpc.QuotaFailure"

	maxViolationDescriptionLen = 200
)

type accountIDCtxKey struct{}

// withAccountID binds an account ID onto ctx so upstream HTTP and stream handlers record
// rate-limit details per account both under sdk.Serve and in direct unit calls.
func withAccountID(ctx context.Context, accountID string) context.Context {
	if accountID == "" {
		return ctx
	}
	return context.WithValue(ctx, accountIDCtxKey{}, accountID)
}

func accountIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(accountIDCtxKey{}).(string); ok && id != "" {
		return id
	}
	if info, ok := sdk.UpstreamInfo(ctx); ok && info.AccountID != "" {
		return info.AccountID
	}
	return ""
}

// quotaViolation holds the fields parsed from one google.rpc.QuotaFailure.Violation entry.
type quotaViolation struct {
	Subject          string            `json:"subject,omitempty"`
	Description      string            `json:"description,omitempty"`
	APIService       string            `json:"api_service,omitempty"`
	QuotaMetric      string            `json:"quota_metric,omitempty"`
	QuotaID          string            `json:"quota_id,omitempty"`
	QuotaDimensions  map[string]string `json:"quota_dimensions,omitempty"`
	QuotaValue       *float64          `json:"quota_value,omitempty"`
	FutureQuotaValue *float64          `json:"future_quota_value,omitempty"`
}

// rateLimitObservation holds the last HTTP 429 reply seen for an account in extension memory.
type rateLimitObservation struct {
	ObservedAt   time.Time         `json:"observed_at"`
	Model        string            `json:"model,omitempty"`
	Status       string            `json:"status,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	Domain       string            `json:"domain,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	RetryDelayMS int64             `json:"retry_delay_ms,omitempty"`
	ResetsAt     int64             `json:"resets_at,omitempty"`
	Violations   []quotaViolation  `json:"violations,omitempty"`
}

type rateLimitStore struct {
	mu sync.RWMutex
	by map[string]rateLimitObservation
}

var lastRateLimits = &rateLimitStore{by: map[string]rateLimitObservation{}}

func (s *rateLimitStore) set(accountID string, obs rateLimitObservation) {
	if accountID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.by == nil {
		s.by = map[string]rateLimitObservation{}
	}
	s.by[accountID] = obs
}

func (s *rateLimitStore) get(accountID string) (rateLimitObservation, bool) {
	if accountID == "" {
		return rateLimitObservation{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	obs, ok := s.by[accountID]
	return obs, ok
}

// googleErrorDetail is a detail object from Google's RPC error envelope.
type googleErrorDetail struct {
	Type       string            `json:"@type"`
	Reason     string            `json:"reason"`
	Domain     string            `json:"domain"`
	Metadata   map[string]string `json:"metadata"`
	RetryDelay string            `json:"retryDelay"`
	Violations []struct {
		Subject          string            `json:"subject"`
		Description      string            `json:"description"`
		APIService       string            `json:"apiService"`
		QuotaMetric      string            `json:"quotaMetric"`
		QuotaID          string            `json:"quotaId"`
		QuotaDimensions  map[string]string `json:"quotaDimensions"`
		QuotaValue       json.RawMessage   `json:"quotaValue"`
		FutureQuotaValue json.RawMessage   `json:"futureQuotaValue"`
	} `json:"violations"`
}

// googleError is the error body Google's APIs answer with (object or single-element array).
type googleError struct {
	Error struct {
		Code    int                 `json:"code"`
		Message string              `json:"message"`
		Status  string              `json:"status"`
		Details []googleErrorDetail `json:"details"`
	} `json:"error"`
}

// parseGoogleError unmarshals either {"error": ...} or [{"error": ...}].
func parseGoogleError(raw []byte) (googleError, bool) {
	var single googleError
	if json.Unmarshal(raw, &single) == nil && (single.Error.Code != 0 || single.Error.Status != "" || single.Error.Message != "" || len(single.Error.Details) > 0) {
		return single, true
	}
	var list []googleError
	if json.Unmarshal(raw, &list) == nil && len(list) > 0 {
		return list[0], true
	}
	return googleError{}, false
}

// reason returns the ErrorInfo reason in the error's details, such as API_KEY_INVALID.
func (g *googleError) reason() string {
	for _, d := range g.Error.Details {
		if d.Reason != "" && (d.Type == "" || d.Type == typeErrorInfo) {
			return d.Reason
		}
	}
	return ""
}

// retryDelay returns the RetryInfo delay (or ErrorInfo metadata.quotaResetDelay fallback) in
// milliseconds, or 0.
func (g *googleError) retryDelay() int64 {
	for _, d := range g.Error.Details {
		if d.RetryDelay == "" || (d.Type != "" && d.Type != typeRetryInfo) {
			continue
		}
		if dur, err := parseGoogleDuration(d.RetryDelay); err == nil && dur > 0 {
			return dur.Milliseconds()
		}
	}
	for _, d := range g.Error.Details {
		if d.Type != "" && d.Type != typeErrorInfo {
			continue
		}
		if v := strings.TrimSpace(d.Metadata["quotaResetDelay"]); v != "" {
			if dur, err := parseGoogleDuration(v); err == nil && dur > 0 {
				return dur.Milliseconds()
			}
		}
	}
	return 0
}

func parseGoogleDuration(raw string) (time.Duration, error) {
	return time.ParseDuration(strings.ToLower(strings.TrimSpace(raw)))
}

// parseQuotaNumber parses a proto3 JSON int64 (string or number) into a non-negative float64.
func parseQuotaNumber(raw json.RawMessage) *float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var num float64
	if err := json.Unmarshal(raw, &num); err == nil {
		if !math.IsNaN(num) && !math.IsInf(num, 0) && num >= 0 {
			return &num
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if v, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
			return &v
		}
	}
	return nil
}

// observeRateLimit records an HTTP 429 (or embedded 429 error) for the account named on ctx.
func observeRateLimit(ctx context.Context, header http.Header, raw []byte, parsed *googleError, hasParsed bool, model string, redact *protocol.Redactor, now time.Time) {
	acctID := accountIDFromContext(ctx)
	if acctID == "" {
		return
	}
	var g googleError
	if hasParsed && parsed != nil {
		g = *parsed
	} else {
		g, _ = parseGoogleError(raw)
	}
	obs := rateLimitObservation{
		ObservedAt: now.UTC(),
		Model:      redact.String(model),
		Status:     redact.String(g.Error.Status),
	}
	for _, d := range g.Error.Details {
		switch {
		case d.Type == typeErrorInfo || (d.Type == "" && d.Reason != ""):
			if obs.Reason == "" {
				obs.Reason = redact.String(d.Reason)
				obs.Domain = redact.String(d.Domain)
				if len(d.Metadata) > 0 {
					obs.Metadata = make(map[string]string, len(d.Metadata))
					for k, v := range d.Metadata {
						obs.Metadata[redact.String(k)] = redact.String(v)
					}
				}
			}
		case d.Type == typeQuotaFailure || (d.Type == "" && len(d.Violations) > 0):
			for _, v := range d.Violations {
				qv := quotaViolation{
					Subject:          redact.String(v.Subject),
					Description:      truncateString(redact.String(v.Description), maxViolationDescriptionLen),
					APIService:       redact.String(v.APIService),
					QuotaMetric:      redact.String(v.QuotaMetric),
					QuotaID:          redact.String(v.QuotaID),
					QuotaValue:       parseQuotaNumber(v.QuotaValue),
					FutureQuotaValue: parseQuotaNumber(v.FutureQuotaValue),
				}
				if len(v.QuotaDimensions) > 0 {
					qv.QuotaDimensions = make(map[string]string, len(v.QuotaDimensions))
					for dk, dv := range v.QuotaDimensions {
						qv.QuotaDimensions[redact.String(dk)] = redact.String(dv)
					}
				}
				obs.Violations = append(obs.Violations, qv)
			}
		}
	}
	delayMS := g.retryDelay()
	if delayMS == 0 && header != nil {
		delayMS = retryAfter(header.Get("Retry-After"), now)
	}
	obs.RetryDelayMS = delayMS
	if delayMS > 0 {
		obs.ResetsAt = obs.ObservedAt.Unix() + (delayMS+999)/1000
	}
	lastRateLimits.set(acctID, obs)
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

// quota returns what is known about the account's limits from its most recent HTTP 429 reply in
// extension memory: Google offers no quota reading endpoint or rate-limit headers for Gemini API
// keys or Vertex AI accounts.
func quota(_ context.Context, call sdk.QuotaCall) (*protocol.Quota, error) {
	if _, err := readSettings(call.Account.Settings); err != nil {
		return nil, err
	}
	obs, ok := lastRateLimits.get(call.Account.ID)
	if !ok {
		raw, _ := json.Marshal(struct {
			Source   string `json:"source"`
			Observed bool   `json:"observed"`
			Note     string `json:"note"`
		}{
			Source:   "last_429",
			Observed: false,
			Note:     "Google offers no quota reading for Gemini or Vertex accounts; no 429 rate-limit reply has been seen for this account yet.",
		})
		return &protocol.Quota{
			Plan:   "No rate-limit reply seen yet (last-seen only; Google offers no quota reading)",
			Limits: []protocol.QuotaLimit{},
			Raw:    raw,
		}, nil
	}

	observedStr := obs.ObservedAt.UTC().Format(time.RFC3339)
	plan := fmt.Sprintf("Last 429 at %s (last-seen value, not a live reading)", observedStr)
	if obs.Reason != "" {
		plan = fmt.Sprintf("Last 429 (%s) at %s (last-seen value, not a live reading)", obs.Reason, observedStr)
	}

	limits := make([]protocol.QuotaLimit, 0, max(1, len(obs.Violations)))
	seenIDs := map[string]bool{}
	uniqueID := func(base string) string {
		if base == "" {
			base = "rate_limit"
		}
		if !seenIDs[base] {
			seenIDs[base] = true
			return base
		}
		for n := 2; ; n++ {
			cand := fmt.Sprintf("%s#%d", base, n)
			if !seenIDs[cand] {
				seenIDs[cand] = true
				return cand
			}
		}
	}

	metaLimit := (*float64)(nil)
	if len(obs.Metadata) > 0 {
		if rawVal := strings.TrimSpace(obs.Metadata["quota_limit_value"]); rawVal != "" {
			if v, err := strconv.ParseFloat(rawVal, 64); err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 {
				metaLimit = &v
			}
		}
	}

	if len(obs.Violations) > 0 {
		for _, v := range obs.Violations {
			name := firstNonEmpty(v.QuotaID, v.QuotaMetric, v.Description, obs.Metadata["quota_limit"], obs.Reason, "Rate limit")
			idBase := firstNonEmpty(v.QuotaID, v.QuotaMetric, "rate_limit")
			if loc := v.QuotaDimensions["location"]; loc != "" && seenIDs[idBase] {
				idBase = idBase + ":" + loc
			}
			lim := protocol.QuotaLimit{
				ID:            uniqueID(idBase),
				Label:         fmt.Sprintf("%s (last seen from 429 at %s; not a live reading)", name, observedStr),
				ResetsAt:      obs.ResetsAt,
				WindowSeconds: inferWindowSeconds(v.QuotaID, v.QuotaMetric, v.Description),
			}
			if v.QuotaValue != nil {
				val := *v.QuotaValue
				lim.Limit = &val
				lim.Unit = inferQuotaUnit(v.QuotaMetric, v.QuotaID, v.Description)
			} else if len(obs.Violations) == 1 && metaLimit != nil {
				val := *metaLimit
				lim.Limit = &val
				lim.Unit = inferQuotaUnit(v.QuotaMetric, v.QuotaID, obs.Metadata["quota_limit"], obs.Metadata["quota_metric"])
			}
			if m := firstNonEmpty(v.QuotaDimensions["model"], obs.Metadata["model"], obs.Model); m != "" {
				lim.Models = []string{m}
			}
			limits = append(limits, lim)
		}
	} else {
		name := firstNonEmpty(obs.Metadata["quota_limit"], obs.Metadata["quota_metric"], obs.Reason, obs.Status, "Rate limit")
		idBase := firstNonEmpty(obs.Metadata["quota_limit"], obs.Metadata["quota_metric"], strings.ToLower(obs.Reason), "rate_limit")
		lim := protocol.QuotaLimit{
			ID:            uniqueID(idBase),
			Label:         fmt.Sprintf("%s (last seen from 429 at %s; not a live reading)", name, observedStr),
			ResetsAt:      obs.ResetsAt,
			WindowSeconds: inferWindowSeconds(obs.Metadata["quota_limit"], obs.Metadata["quota_metric"]),
		}
		if metaLimit != nil {
			val := *metaLimit
			lim.Limit = &val
			lim.Unit = inferQuotaUnit(obs.Metadata["quota_limit"], obs.Metadata["quota_metric"])
		}
		if m := firstNonEmpty(obs.Metadata["model"], obs.Model); m != "" {
			lim.Models = []string{m}
		}
		limits = append(limits, lim)
	}

	type errorInfoRaw struct {
		Reason   string            `json:"reason,omitempty"`
		Domain   string            `json:"domain,omitempty"`
		Metadata map[string]string `json:"metadata,omitempty"`
	}
	var errInfo *errorInfoRaw
	if obs.Reason != "" || obs.Domain != "" || len(obs.Metadata) > 0 {
		errInfo = &errorInfoRaw{Reason: obs.Reason, Domain: obs.Domain, Metadata: maps.Clone(obs.Metadata)}
	}
	raw, _ := json.Marshal(struct {
		Source       string           `json:"source"`
		Observed     bool             `json:"observed"`
		ObservedAt   time.Time        `json:"observed_at"`
		Note         string           `json:"note"`
		Model        string           `json:"model,omitempty"`
		Status       string           `json:"status,omitempty"`
		RetryDelayMS int64            `json:"retry_delay_ms,omitempty"`
		ResetsAt     int64            `json:"resets_at,omitempty"`
		ErrorInfo    *errorInfoRaw    `json:"error_info,omitempty"`
		Violations   []quotaViolation `json:"violations,omitempty"`
	}{
		Source:       "last_429",
		Observed:     true,
		ObservedAt:   obs.ObservedAt,
		Note:         "Last-seen values from the most recent HTTP 429 rate-limit reply; Google offers no live quota reading.",
		Model:        obs.Model,
		Status:       obs.Status,
		RetryDelayMS: obs.RetryDelayMS,
		ResetsAt:     obs.ResetsAt,
		ErrorInfo:    errInfo,
		Violations:   obs.Violations,
	})

	return &protocol.Quota{
		Plan:   plan,
		Limits: limits,
		Raw:    raw,
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func inferQuotaUnit(texts ...string) string {
	for _, s := range texts {
		low := strings.ToLower(s)
		switch {
		case strings.Contains(low, "token"):
			return "tokens"
		case strings.Contains(low, "request"):
			return "requests"
		}
	}
	return ""
}

func inferWindowSeconds(texts ...string) int64 {
	for _, s := range texts {
		low := strings.ToLower(s)
		switch {
		case strings.Contains(low, "perminute") || strings.Contains(low, "per_minute") || strings.Contains(low, "per minute"):
			return 60
		case strings.Contains(low, "perday") || strings.Contains(low, "per_day") || strings.Contains(low, "daily") || strings.Contains(low, "per day"):
			return 86400
		case strings.Contains(low, "perhour") || strings.Contains(low, "per_hour") || strings.Contains(low, "per hour"):
			return 3600
		case strings.Contains(low, "persecond") || strings.Contains(low, "per_second") || strings.Contains(low, "per second"):
			return 1
		}
	}
	return 0
}
