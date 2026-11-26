package main

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
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
		g, err := grantFrom(&tokenResponse{AccessToken: "opaque", RefreshToken: "r", IDToken: codextest.IDToken(), ExpiresIn: tc.expiresIn}, nil)
		if err != nil {
			t.Fatal(err)
		}
		c := g.Credential
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

// A JWT access token carries its own expiry; an absurd one is bounded the same way.
func TestJWTExpiryIsBounded(t *testing.T) {
	seg := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
	access := seg(`{"alg":"none"}`) + "." + seg(`{"exp":9223372036854775807}`) + "." + seg("sig")
	before := time.Now()
	g, err := grantFrom(&tokenResponse{AccessToken: access, RefreshToken: "r", IDToken: codextest.IDToken()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hi := before.Add(sdk.MaxTokenLife).Unix() + 5; g.Credential.ExpiresAt < before.Unix() || g.Credential.ExpiresAt > hi {
		t.Errorf("expires_at = %d, want between %d and %d", g.Credential.ExpiresAt, before.Unix(), hi)
	}
}
