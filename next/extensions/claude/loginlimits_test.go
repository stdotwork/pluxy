package main

import (
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/sdk"
)

// An access token's lifetime from the server is bounded before it becomes an expiry time: a huge
// one must not overflow into the past (renewal churn), a negative one must not expire the token.
func TestTokenLifetimeIsBounded(t *testing.T) {
	before := time.Now()
	for _, tc := range []struct {
		name      string
		expiresIn int64
		valid     bool
	}{{"negative", -5, false}, {"max int64", 1<<63 - 1, true}, {"wraps int64", 9_300_000_000, true}} {
		c := (&tokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: tc.expiresIn}).credential(time.Now())
		if !tc.valid {
			if c.ExpiresAt != 0 || c.RefreshAt != 0 {
				t.Errorf("%s: expires_at %d, refresh_at %d, want none", tc.name, c.ExpiresAt, c.RefreshAt)
			}
			continue
		}
		lo, hi := before.Unix(), before.Add(sdk.MaxTokenLife).Unix()+5
		if c.ExpiresAt < lo || c.ExpiresAt > hi {
			t.Errorf("%s: expires_at = %d, want between %d and %d", tc.name, c.ExpiresAt, lo, hi)
		}
		if c.RefreshAt != 0 && (c.RefreshAt < lo || c.RefreshAt > c.ExpiresAt) {
			t.Errorf("%s: refresh_at = %d, want between %d and expires_at %d", tc.name, c.RefreshAt, lo, c.ExpiresAt)
		}
	}
}
