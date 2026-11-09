package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
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

// closedURL returns the address of a port nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
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

func status(code int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// An API key login is checked by one request. It is retryable only when the request never reached
// Google, or when Google rate limited it.
func TestAPIKeyLoginRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", closedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", loginServer(t, hangUp), llm.CodeUnavailable, false},
		{"500", loginServer(t, status(500, `{}`)), llm.CodeUpstream, false},
		{"503", loginServer(t, status(503, `{}`)), llm.CodeUnavailable, false},
		{"429", loginServer(t, status(429, `{}`)), llm.CodeRateLimit, true},
		{"key refused", loginServer(t, status(400, `{"error":{"status":"INVALID_ARGUMENT","message":"API key not valid"}}`)), llm.CodeAuth, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := apiKeyLogin(context.Background(), settings{BaseURL: c.url}, "key")
			wantError(t, err, c.code, c.retryable)
		})
	}
}

// A service account login fetches an access token. The fetch is shared with model requests, whose
// errors are retryable because the model request did not run; the login follows its own rule.
func TestServiceAccountLoginRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", closedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", loginServer(t, hangUp), llm.CodeUnavailable, false},
		{"503", loginServer(t, status(503, `{}`)), llm.CodeUnavailable, false},
		{"429", loginServer(t, status(429, `{}`)), llm.CodeUnavailable, true},
		{"key refused", loginServer(t, status(400, `{"error":"invalid_grant"}`)), llm.CodeAuth, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tokens = &tokenCache{entries: map[string]*tokenEntry{}}
			var raw map[string]any
			_ = json.Unmarshal(geminitest.New(t).ServiceAccount("p", true), &raw)
			raw["token_uri"] = c.url + "/token"
			key, _ := json.Marshal(raw)
			set := settings{Vertex: &vertexSettings{Project: "p"}}
			_, err := serviceAccountLogin(context.Background(), set, string(key))
			wantError(t, err, c.code, c.retryable)
		})
	}
}

// The token fetch for a model request is unchanged: it did not reach the model, so a failure to
// fetch the token is retryable on another account whether or not the token request was sent.
func TestModelRequestTokenFetchStaysRetryable(t *testing.T) {
	for name, url := range map[string]string{
		"hung up": loginServer(t, hangUp),
		"503":     loginServer(t, status(503, `{}`)),
	} {
		t.Run(name, func(t *testing.T) {
			var raw map[string]any
			_ = json.Unmarshal(geminitest.New(t).ServiceAccount("p", true), &raw)
			raw["token_uri"] = url + "/token"
			b, _ := json.Marshal(raw)
			sa, err := readServiceAccount(b)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = fetchToken(context.Background(), sa)
			wantError(t, err, llm.CodeUnavailable, true)
		})
	}
}
