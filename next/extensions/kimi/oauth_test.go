package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/kimi/kimitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

type noPrompt struct{}

func (noPrompt) Prompt(protocol.Prompt) error { return nil }

func settingsOf(t *testing.T, raw json.RawMessage) settings {
	t.Helper()
	s, err := readSettings(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Login stops when the device code expires, even when slow_down has pushed the next poll past
// the code's end.
func TestDeviceCodeExpiryStopsPolling(t *testing.T) {
	srv := kimitest.New(t)
	srv.CodeLifetime, srv.SlowDown = 3, true
	start := time.Now()
	_, err := deviceLogin(context.Background(), settingsOf(t, srv.Settings()), noPrompt{})
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential {
		t.Fatalf("err = %v, want an auth error for the expired code", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("login took %v for a code that expired after 3s", d)
	}
}

// A login host that stops answering fails a renewal instead of holding it.
func TestRenewalTimesOut(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	hung := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hung:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(hung)
	old := authClient.Timeout
	if old <= 0 || old > time.Minute {
		t.Fatalf("auth client timeout %v, want a bound of at most a minute", old)
	}
	authClient.Timeout = 200 * time.Millisecond
	defer func() { authClient.Timeout = old }()
	acct := protocol.Account{Settings: json.RawMessage(`{"auth_base_url":"` + ts.URL + `"}`),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}
	done := make(chan error, 1)
	go func() { _, err := refresh(context.Background(), sdk.RefreshCall{Account: acct}); done <- err }()
	select {
	case err := <-done:
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("err = %v, want a retryable unavailable error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the renewal waits for a login host that never answers")
	}
}

// A renewal that fails for now keeps the login host's Retry-After, in seconds or as a date.
func TestRenewalRetryAfter(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	for _, c := range []struct {
		status int
		header string
		min    int64
	}{
		{http.StatusServiceUnavailable, "900", 900_000},
		{http.StatusTooManyRequests, time.Now().Add(10 * time.Minute).UTC().Format(http.TimeFormat), 500_000},
	} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", c.header)
			w.WriteHeader(c.status)
		}))
		acct := protocol.Account{Settings: json.RawMessage(`{"auth_base_url":"` + ts.URL + `"}`),
			Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a", RefreshToken: "r"}}
		_, err := refresh(context.Background(), sdk.RefreshCall{Account: acct})
		ts.Close()
		var e *llm.Error
		if !errors.As(err, &e) || !e.Retryable || e.RetryAfterMS < c.min {
			t.Errorf("%d with Retry-After %q: %+v, want retryable after at least %dms", c.status, c.header, err, c.min)
		}
	}
}
