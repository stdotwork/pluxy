package sdk

import (
	"math"
	"testing"
	"time"
)

func TestBoundSeconds(t *testing.T) {
	for _, tc := range []struct {
		secs int64
		max  time.Duration
		want time.Duration
	}{
		{-5, time.Hour, 0},
		{0, time.Hour, 0},
		{7, time.Hour, 7 * time.Second},
		{3600, time.Hour, time.Hour},
		{3601, time.Hour, time.Hour},
		{math.MaxInt64, time.Hour, time.Hour},
		{math.MinInt64, time.Hour, 0},
		{9_300_000_000, time.Hour, time.Hour}, // times time.Second this wraps int64
	} {
		if got := BoundSeconds(tc.secs, tc.max); got != tc.want {
			t.Errorf("BoundSeconds(%d, %s) = %s, want %s", tc.secs, tc.max, got, tc.want)
		}
	}
}

func TestExpiryUnix(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		expiresIn int64
		want      int64
	}{
		{-1, 0},
		{0, 0},
		{3600, 1_700_003_600},
		{math.MaxInt64, 1_700_000_000 + 365*86400},
		{9_300_000_000, 1_700_000_000 + 365*86400},
	} {
		if got := ExpiryUnix(now, tc.expiresIn); got != tc.want {
			t.Errorf("ExpiryUnix(%d) = %d, want %d", tc.expiresIn, got, tc.want)
		}
	}
}
