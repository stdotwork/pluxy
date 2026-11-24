package sdk

import (
	"net/http"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	date := func(d time.Duration) string { return now.Add(d).Format(http.TimeFormat) }
	for _, tc := range []struct {
		name  string
		value string
		want  int64
	}{
		{"empty", "", 0},
		{"seconds", "20", 20000},
		{"zero seconds", "0", 0},
		{"one second", "1", 1000},
		{"plus sign", "+5", 5000},
		{"leading zeros", "007", 7000},
		{"seconds with spaces", " 12 ", 12000},
		{"negative seconds", "-5", 0},
		{"fractional seconds", "1.5", 0},
		{"a month of seconds", "2592000", 2592000000},
		{"text", "soon", 0},
		{"spaces only", "   ", 0},
		{"HTTP date ahead", date(90 * time.Second), 90000},
		{"HTTP date a day ahead", date(24 * time.Hour), 86400000},
		{"HTTP date at now", date(0), 0},
		{"HTTP date in the past", date(-time.Minute), 0},
		{"HTTP date with spaces is not read", " " + date(time.Minute) + " ", 0},
		{"RFC 850 date ahead", now.Add(time.Minute).Format(time.RFC850), 60000},
		{"ANSI C date ahead", now.Add(time.Minute).Format(time.ANSIC), 60000},
		{"RFC 3339 is not an HTTP date", now.Add(time.Minute).Format(time.RFC3339), 0},
	} {
		if got := RetryAfter(tc.value, now); got != tc.want {
			t.Errorf("%s: RetryAfter(%q) = %d, want %d", tc.name, tc.value, got, tc.want)
		}
	}
	// Dates have whole-second precision: a now part-way through a second rounds the wait down.
	if got := RetryAfter(date(10*time.Second), now.Add(500*time.Millisecond)); got != 9500 {
		t.Errorf("date measured from a fractional now = %d, want 9500", got)
	}
}
