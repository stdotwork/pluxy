package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The numbers a login server sends (interval, expires_in) are bounded before they become
// durations: a huge one must not overflow, a negative one must not make a tight polling loop or
// end the login at once.
func TestDeviceCodeNumbersAreBounded(t *testing.T) {
	const (
		maxInt64 = "9223372036854775807"
		wraps    = "9300000000" // seconds; times time.Second this wraps int64
		longLife = "9000000000" // 285 years: positive as a duration, but absurd
	)
	for _, tc := range []struct {
		name, interval, expiresIn string
		wantWait                  time.Duration
	}{
		{"negative", "-5", "-5", defaultPollInterval},
		{"max int64", maxInt64, maxInt64, sdk.MaxPollInterval},
		{"wraps int64", wraps, wraps, sdk.MaxPollInterval},
		{"285 years", longLife, longLife, sdk.MaxPollInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			polls := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/oauth/device_authorization":
					fmt.Fprintf(w, `{"device_code":"dc","user_code":"UC","verification_uri":"https://example.invalid/verify","expires_in":%s,"interval":%s}`, tc.expiresIn, tc.interval)
				case "/api/oauth/token":
					mu.Lock()
					polls++
					n := polls
					mu.Unlock()
					w.WriteHeader(http.StatusBadRequest)
					if n == 1 {
						io.WriteString(w, `{"error":"authorization_pending"}`)
						return
					}
					io.WriteString(w, `{"error":"access_denied"}`)
				}
			}))
			defer ts.Close()
			t.Setenv(protocol.EnvTestLoginHosts, "1")
			set, err := readSettings([]byte(`{"auth_base_url":"` + ts.URL + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			var waits []time.Duration
			old := waitPoll
			waitPoll = func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }
			defer func() { waitPoll = old }()
			var shown protocol.Prompt
			_, err = deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { shown = p }))
			var e *llm.Error
			if !errors.As(err, &e) || e.Message != "the sign-in was denied" {
				t.Fatalf("err = %v, want the second poll's access_denied (the code expired too early?)", err)
			}
			if len(waits) != 2 || waits[0] != tc.wantWait {
				t.Errorf("waits between polls = %v, want two of %s", waits, tc.wantWait)
			}
			if shown.ExpiresIn < 0 || shown.ExpiresIn > int64(sdk.MaxLoginWait/time.Second) {
				t.Errorf("the prompt says the code lives %d seconds, want 0 to %d", shown.ExpiresIn, int64(sdk.MaxLoginWait/time.Second))
			}
		})
	}
}

type promptFunc func(protocol.Prompt)

func (f promptFunc) Prompt(p protocol.Prompt) error { f(p); return nil }

// An access token's lifetime from the server is bounded before it becomes an expiry time.
func TestTokenLifetimeIsBounded(t *testing.T) {
	before := time.Now()
	for _, tc := range []struct {
		name      string
		expiresIn int64
		valid     bool
	}{{"negative", -5, false}, {"max int64", 1<<63 - 1, true}, {"wraps int64", 9_300_000_000, true}} {
		c := (&tokenResponse{AccessToken: "a", RefreshToken: "r", ExpiresIn: tc.expiresIn}).credential("dev", "")
		if !tc.valid {
			if c.ExpiresAt != 0 || c.RefreshAt != 0 {
				t.Errorf("%s: expires_at %d, refresh_at %d, want none", tc.name, c.ExpiresAt, c.RefreshAt)
			}
			continue
		}
		if lo, hi := before.Unix(), before.Add(sdk.MaxTokenLife).Unix()+5; c.ExpiresAt < lo || c.ExpiresAt > hi {
			t.Errorf("%s: expires_at = %d, want between %d and %d", tc.name, c.ExpiresAt, lo, hi)
		}
	}
}
