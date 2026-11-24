package sdk

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryAfter reads a Retry-After header value as milliseconds from now: a whole number of seconds
// (surrounding spaces ignored) or an HTTP date, which is measured against now. It returns 0 for an
// empty, malformed, negative or fractional value, and for a date that is not after now. Pass the
// header an upstream sent as the value and time.Now() as now; a provider's other hints (a body
// field, a vendor header) stay in the extension, which falls back on this for the standard header.
func RetryAfter(value string, now time.Time) int64 {
	if value == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && secs >= 0 {
		return secs * 1000
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := t.Sub(now); d > 0 {
			return d.Milliseconds()
		}
	}
	return 0
}
