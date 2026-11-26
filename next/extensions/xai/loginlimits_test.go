package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// The numbers a login server sends (interval, expires_in) are bounded before they become
// durations or an expiry time: a huge one must not overflow, a negative one must not make a tight
// polling loop or a past expiry.
func TestServerNumbersAreBounded(t *testing.T) {
	const (
		maxInt64 = "9223372036854775807"
		wraps    = "9300000000" // seconds; times time.Second this wraps int64
		longLife = "9000000000" // 285 years: positive as a duration, but absurd
	)
	for _, tc := range []struct {
		name, interval, expiresIn string
		wantWait                  time.Duration
		wantExpiry                bool // the token reply's expires_in is positive
	}{
		{"negative", "-5", "-5", minPollInterval, false},
		{"max int64", maxInt64, maxInt64, sdk.MaxPollInterval, true},
		{"wraps int64", wraps, wraps, sdk.MaxPollInterval, true},
		{"285 years", longLife, longLife, sdk.MaxPollInterval, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var polls int
			var mu sync.Mutex
			var ts *httptest.Server
			ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					fmt.Fprintf(w, `{"device_authorization_endpoint":"%[1]s/device","token_endpoint":"%[1]s/token"}`, ts.URL)
				case "/device":
					fmt.Fprintf(w, `{"device_code":"dc","user_code":"UC","verification_uri":"%s/verify","expires_in":%s,"interval":%s}`, ts.URL, tc.expiresIn, tc.interval)
				case "/token":
					mu.Lock()
					polls++
					n := polls
					mu.Unlock()
					switch {
					case n == 1:
						w.WriteHeader(http.StatusBadRequest)
						io.WriteString(w, `{"error":"authorization_pending"}`)
					case n == 2:
						fmt.Fprintf(w, `{"access_token":"a","refresh_token":"r","expires_in":%s}`, tc.expiresIn)
					default:
						t.Errorf("poll %d: the login kept polling", n)
						w.WriteHeader(http.StatusBadRequest)
						io.WriteString(w, `{"error":"access_denied"}`)
					}
				}
			}))
			defer ts.Close()
			t.Setenv(protocol.EnvTestLoginHosts, "1")
			set, err := readSettings([]byte(`{"auth_issuer":"` + ts.URL + `"}`))
			if err != nil {
				t.Fatal(err)
			}
			var waits []time.Duration
			old := waitPoll
			waitPoll = func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true }
			defer func() { waitPoll = old }()
			var shown protocol.Prompt
			before := time.Now()
			g, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { shown = p }))
			if err != nil {
				t.Fatal(err)
			}
			if len(waits) != 1 || waits[0] != tc.wantWait {
				t.Errorf("waits between polls = %v, want [%s]", waits, tc.wantWait)
			}
			if shown.ExpiresIn < 0 || shown.ExpiresIn > int64(sdk.MaxLoginWait/time.Second) {
				t.Errorf("the prompt says the code lives %d seconds, want 0 to %d", shown.ExpiresIn, int64(sdk.MaxLoginWait/time.Second))
			}
			c := g.Credential
			if !tc.wantExpiry {
				if c.ExpiresAt != 0 || c.RefreshAt != 0 {
					t.Errorf("a negative lifetime gave expires_at %d, refresh_at %d, want none", c.ExpiresAt, c.RefreshAt)
				}
				return
			}
			if lo, hi := before.Unix(), before.Add(sdk.MaxTokenLife).Unix()+5; c.ExpiresAt < lo || c.ExpiresAt > hi {
				t.Errorf("expires_at = %d, want between %d and %d", c.ExpiresAt, lo, hi)
			}
		})
	}
}
