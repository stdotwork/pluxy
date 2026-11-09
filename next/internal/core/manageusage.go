package core

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/internal/usage"
)

// Limits of the usage calls' parameters.
const (
	defaultUsageBucket  = time.Minute
	defaultUsageBuckets = 60
	maxUsageBuckets     = 1440
	defaultUsagePage    = 100
	maxUsagePage        = 1000
)

// handleUsage adds the usage calls. Reading the usage record never changes it.
//
//	GET /v0/management/usage         totals, breakdowns by model, provider, account and client
//	                                 key, and a time series
//	GET /v0/management/usage/events  the recorded requests, oldest first, a page at a time
func (c *Core) handleUsage(m *server.Management) {
	m.Handle("GET /usage", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, err := usageFilter(q)
		if err != nil {
			writeUsageError(w, err)
			return
		}
		bucket, buckets := defaultUsageBucket, defaultUsageBuckets
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
		writeJSON(w, http.StatusOK, struct {
			Enabled bool `json:"enabled"`
			usage.Summary
		}{c.Config().Usage.On(), c.usage.Summarize(f, bucket, buckets)})
	})
	m.Handle("GET /usage/events", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f, err := usageFilter(q)
		if err != nil {
			writeUsageError(w, err)
			return
		}
		var after uint64
		if v := q.Get("after"); v != "" {
			if after, err = strconv.ParseUint(v, 10, 64); err != nil {
				writeUsageError(w, fmt.Errorf("after %q must be an event's seq", v))
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
		writeJSON(w, http.StatusOK, struct {
			Enabled bool `json:"enabled"`
			usage.Page
		}{c.Config().Usage.On(), c.usage.Events(f, after, limit)})
	})
}

// usageFilter reads the filter parameters both usage calls take: since and until (RFC 3339
// times, or since as a duration back from now, such as 24h), model, provider, account, key_id
// and failed (true or false).
func usageFilter(q url.Values) (usage.Filter, error) {
	f := usage.Filter{Model: q.Get("model"), Provider: q.Get("provider"), Account: q.Get("account"), KeyID: q.Get("key_id")}
	for _, p := range []struct {
		name string
		t    *time.Time
	}{{"since", &f.Since}, {"until", &f.Until}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		if d, err := time.ParseDuration(v); err == nil && p.name == "since" && d > 0 {
			*p.t = time.Now().Add(-d)
			continue
		}
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			if p.name == "since" {
				return f, fmt.Errorf("since %q must be an RFC 3339 time or a duration such as 24h", v)
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

func writeUsageError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request", "message": err.Error()}})
}
