package core

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/internal/usage"
	"github.com/stdotwork/pluxy/next/management"
)

// Limits of the usage calls' parameters.
const (
	maxUsageBuckets  = 1440
	defaultUsagePage = 100
	maxUsagePage     = 1000
)

// handleUsage adds the usage calls. Reading the usage record never changes it.
//
//	GET /v0/management/usage         totals, breakdowns by model, provider, account and client
//	                                 key, and a time series
//	GET /v0/management/usage/events  the recorded requests, oldest first (after) or newest first
//	                                 (before), a page at a time
func (c *Core) handleUsage(m *server.Management) {
	m.Handle("GET /usage", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, err := usageFilter(q)
		if err != nil {
			writeUsageError(w, err)
			return
		}
		by := q.Get("by")
		switch by {
		case "", "model", "account", "key", "provider":
		default:
			writeUsageError(w, fmt.Errorf("by %q must be model, account, key or provider", by))
			return
		}
		var bucket time.Duration
		var buckets int
		if v := q.Get("bucket"); v != "" {
			if bucket, err = time.ParseDuration(v); err != nil || bucket < time.Second || bucket > 24*time.Hour {
				writeUsageError(w, fmt.Errorf("bucket %q must be a duration from 1s to 24h, such as 1m", v))
				return
			}
		}
		if v := q.Get("buckets"); v != "" {
			if buckets, err = strconv.Atoi(v); err != nil || buckets < 1 || buckets > maxUsageBuckets {
				writeUsageError(w, fmt.Errorf("buckets %q must be a number from 1 to %d", v, maxUsageBuckets))
				return
			}
		}
		sum := c.usage.Summarize(f, bucket, buckets)
		switch by {
		case "model":
			sum.By, sum.Breakdown = by, sum.Models
		case "provider":
			sum.By, sum.Breakdown = by, sum.Providers
		case "account":
			sum.By, sum.Breakdown = by, sum.Accounts
		case "key":
			sum.By, sum.Breakdown = by, sum.Keys
		}
		writeJSON(w, http.StatusOK, management.UsageReply{Enabled: c.Config().Usage.On(), Summary: sum})
	})
	m.Handle("GET /usage/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, err := usageFilter(q)
		if err != nil {
			writeUsageError(w, err)
			return
		}
		hasAfter := q.Has("after")
		hasBefore := q.Has("before")
		if hasAfter && hasBefore {
			writeUsageError(w, errors.New("after and before cannot be used together"))
			return
		}
		var after, before uint64
		if v := q.Get("after"); v != "" {
			if after, err = strconv.ParseUint(v, 10, 64); err != nil {
				writeUsageError(w, fmt.Errorf("after %q must be an event's seq", v))
				return
			}
		}
		if v := q.Get("before"); v != "" {
			if before, err = strconv.ParseUint(v, 10, 64); err != nil {
				writeUsageError(w, fmt.Errorf("before %q must be an event's seq", v))
				return
			}
		}
		limit := defaultUsagePage
		if v := q.Get("limit"); v != "" {
			if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > maxUsagePage {
				writeUsageError(w, fmt.Errorf("limit %q must be a number from 1 to %d", v, maxUsagePage))
				return
			}
		}
		var page usage.Page
		if hasBefore {
			page = c.usage.EventsBefore(f, before, limit)
		} else {
			page = c.usage.Events(f, after, limit)
		}
		writeJSON(w, http.StatusOK, management.UsageEventsReply{Enabled: c.Config().Usage.On(), Page: page})
	})
}

// usageFilter reads the filter parameters both usage calls take: since and until (RFC 3339
// times, or since as a duration back from now, such as 24h or 7d), model, provider, account,
// key / key_id and failed (true or false).
func usageFilter(q url.Values) (usage.Filter, error) {
	keyID := q.Get("key_id")
	if k := q.Get("key"); k != "" {
		if keyID != "" && keyID != k {
			return usage.Filter{}, errors.New("key and key_id conflict")
		}
		keyID = k
	}
	f := usage.Filter{Model: q.Get("model"), Provider: q.Get("provider"), Account: q.Get("account"), KeyID: keyID}
	for _, p := range []struct {
		name string
		t    *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		if p.name == "since" {
			if d, ok := parseSinceDuration(v); ok {
				*p.t = time.Now().Add(-d)
				continue
			}
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if p.name == "since" {
				return f, fmt.Errorf("since %q must be an RFC 3339 time or a duration such as 24h or 7d", v)
			}
			return f, fmt.Errorf("until %q must be an RFC 3339 time", v)
		}
		*p.t = t
	}
	if v := q.Get("failed"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return f, fmt.Errorf("failed %q must be true or false", v)
		}
		f.Failed = &b
	}
	return f, nil
}

func parseSinceDuration(v string) (time.Duration, bool) {
	if d, err := time.ParseDuration(v); err == nil {
		return d, d > 0
	}
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseInt(days, 10, 64)
		if err == nil && n > 0 && n <= 100000 {
			return time.Duration(n) * 24 * time.Hour, true
		}
	}
	return 0, false
}

func writeUsageError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": err.Error()}})
}
