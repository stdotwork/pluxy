package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/antigravity/agtest"
	"github.com/stdotwork/pluxy/next/llm"
)

func wantError(t *testing.T, err error, code string, retryable bool) {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("got %v, want an *llm.Error", err)
	}
	if e.Code != code || e.Retryable != retryable {
		t.Errorf("got code %s retryable %v, want code %s retryable %v (%s)", e.Code, e.Retryable, code, retryable, e.Message)
	}
}

// closedURL returns the address of a port nothing listens on: port 1 on loopback, which no test
// server can be given, unlike a just-closed ephemeral port that the next httptest server may reuse.
func closedURL(t *testing.T) string {
	t.Helper()
	const addr = "127.0.0.1:1"
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatalf("something listens on %s; the connection-refused cases need it closed", addr)
	}
	return "http://" + addr
}

// hangUp closes the connection of the request being answered without a reply.
func hangUp(w http.ResponseWriter) {
	c, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		c.Close()
	}
}

// loginServer is a server whose every reply is answer.
func loginServer(t *testing.T, answer func(w http.ResponseWriter)) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { answer(w) }))
	t.Cleanup(srv.Close)
	return srv.URL
}

func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, `{"error":"down"}`, code) }
}

func brokenReply(w http.ResponseWriter) {
	w.Header().Set("Content-Length", "100")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, `{"access_token":`)
	hangUp(w)
}

// retryCases are the ways a login call to Google can fail, and what each means for a retry: only
// a request that never reached Google is retryable, apart from a 429, where Google refused to run
// it.
func retryCases(t *testing.T) []struct {
	name      string
	url       string
	code      string
	retryable bool
} {
	return []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", closedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", loginServer(t, hangUp), llm.CodeUnavailable, false},
		{"503", loginServer(t, status(503)), llm.CodeUnavailable, false},
		{"429", loginServer(t, status(429)), llm.CodeRateLimit, true},
		{"reply broke off", loginServer(t, brokenReply), llm.CodeUnavailable, false},
	}
}

func TestTokenRequestRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range retryCases(t) {
		t.Run(c.name, func(t *testing.T) {
			_, err := tokenRequest(context.Background(), c.url+"/token", url.Values{"grant_type": {"refresh_token"}})
			wantError(t, err, c.code, c.retryable)
		})
	}
}

func TestFetchEmailRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range retryCases(t) {
		t.Run(c.name, func(t *testing.T) {
			_, err := fetchEmail(context.Background(), c.url+"/userinfo", "token", "agent")
			wantError(t, err, c.code, c.retryable)
		})
	}
}

func TestFindProjectRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range retryCases(t) {
		t.Run(c.name, func(t *testing.T) {
			_, err := findProject(context.Background(), settings{BaseURL: c.url}, "token")
			wantError(t, err, c.code, c.retryable)
		})
	}
}

// Onboarding that does not finish is not retryable: the onboardUser calls reached Google.
func TestOnboardingNeverFinishingIsNotRetryable(t *testing.T) {
	srv := agtest.New(t)
	srv.NoProject, srv.OnboardPolls = true, 1000
	access, _ := srv.Grant()
	set, _ := readSettings(srv.Settings())
	_, err := findProject(context.Background(), set, access)
	wantError(t, err, llm.CodeUnavailable, false)
}
