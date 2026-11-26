package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/sdk"
)

// An access token's lifetime from the token endpoint is bounded before it becomes an expiry time:
// a huge one must not overflow into the past (the token would be fetched again on every call), a
// negative one must not expire the token, and none means an hour.
func TestTokenLifetimeIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expiresIn int64
		min, max  time.Duration
	}{
		{"negative", -5, time.Hour - time.Minute, time.Hour + time.Minute},
		{"max int64", 1<<63 - 1, sdk.MaxTokenLife - time.Minute, sdk.MaxTokenLife + time.Minute},
		{"wraps int64", 9_300_000_000, sdk.MaxTokenLife - time.Minute, sdk.MaxTokenLife + time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"access_token":"tok","expires_in":%d}`, tc.expiresIn)
			}))
			defer ts.Close()
			var raw map[string]any
			_ = json.Unmarshal(geminitest.New(t).ServiceAccount("p", true), &raw)
			raw["token_uri"] = ts.URL + "/token"
			b, _ := json.Marshal(raw)
			sa, err := readServiceAccount(b)
			if err != nil {
				t.Fatal(err)
			}
			_, expires, err := fetchToken(context.Background(), sa)
			if err != nil {
				t.Fatal(err)
			}
			if life := time.Until(expires); life < tc.min || life > tc.max {
				t.Errorf("the token lives %s, want between %s and %s", life, tc.min, tc.max)
			}
		})
	}
}
