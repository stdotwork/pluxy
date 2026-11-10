package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// vertexCall returns a generate call for a Vertex service account at srv.
func vertexCall(t *testing.T, srv *geminitest.Server) sdk.Call {
	t.Helper()
	return sdk.Call{
		Account: protocol.Account{ID: "v", Settings: settingsFor(srv, kindVertexSA),
			Credential: &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}},
		Request: llm.Request{Model: "gemini-x", Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: []byte(`{"contents":[]}`)}},
	}
}

// An upstream that echoes the request's bearer token in an error does not get it to the client:
// core cannot redact a token it never saw.
func TestFetchedTokenRedacted(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	srv := geminitest.New(t)
	var sent string
	srv.Generate = func(w http.ResponseWriter, r *http.Request, _ geminitest.Request) {
		sent = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400, "message": "bad request with token " + sent, "status": "INVALID_ARGUMENT"}})
	}
	err := generate(context.Background(), vertexCall(t, srv), nil)
	var e *llm.Error
	if !errors.As(err, &e) || sent == "" {
		t.Fatalf("err = %v, token sent %q", err, sent)
	}
	if strings.Contains(e.Message, sent) || strings.Contains(string(e.Raw), sent) {
		t.Errorf("the access token reached the error: %q %s", e.Message, e.Raw)
	}
}

// A token the upstream refuses is not used again.
func TestRefusedTokenFetchedAgain(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	srv := geminitest.New(t)
	call := vertexCall(t, srv)
	s, _ := readSettings(call.Account.Settings)
	first, err := resolve(context.Background(), s, call.Account.Credential, "gemini-x", false)
	if err != nil {
		t.Fatal(err)
	}
	srv.Revoke(first.token)
	err = generate(context.Background(), call, nil)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeAuth {
		t.Fatalf("revoked token: %v, want an auth error", err)
	}
	next, err := resolve(context.Background(), s, call.Account.Credential, "gemini-x", false)
	if err != nil {
		t.Fatal(err)
	}
	if next.token == first.token || srv.Grants() != 2 {
		t.Errorf("after a 401 the same token is used again (%d grants)", srv.Grants())
	}
}

// A call waiting for another call's token fetch can be cancelled.
func TestTokenWaitCancellable(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	hung := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hung:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(hung)
	srv := geminitest.New(t)
	var raw map[string]any
	_ = json.Unmarshal(srv.ServiceAccount("p", true), &raw)
	raw["token_uri"] = ts.URL + "/token"
	b, _ := json.Marshal(raw)
	sa, err := readServiceAccount(b)
	if err != nil {
		t.Fatal(err)
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	go tokens.get(ctxA, sa)
	for {
		tokens.mu.Lock()
		e := tokens.entries[sa.cacheKey()]
		tokens.mu.Unlock()
		if e != nil && len(e.turn) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ctxB, cancelB := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelB()
	done := make(chan error, 1)
	go func() { _, err := tokens.get(ctxB, sa); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want the waiting call's deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled call still waits for another call's token fetch")
	}
}

// Keys whose token fetch failed, and expired tokens, leave the cache.
func TestTokenCacheDropsUnusable(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	srv := geminitest.New(t)
	for _, accepted := range []bool{false, false, true} {
		sa, err := readServiceAccount(srv.ServiceAccount("p", accepted))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = tokens.get(context.Background(), sa)
	}
	if n := len(tokens.entries); n != 1 {
		t.Errorf("%d cache entries, want 1 (the key with a token)", n)
	}
	for _, e := range tokens.entries {
		e.expires = time.Now().Add(-time.Second)
	}
	sa, _ := readServiceAccount(srv.ServiceAccount("p", false))
	_, _ = tokens.get(context.Background(), sa)
	if n := len(tokens.entries); n != 0 {
		t.Errorf("%d cache entries after the token expired, want 0", n)
	}
}

// A key file names where its signed assertion goes, so only Google's token endpoints are taken;
// a fake one only under a test.
func TestTokenURIOnlyGoogle(t *testing.T) {
	srv := geminitest.New(t)
	t.Setenv(protocol.EnvTestLoginHosts, "")
	for uri, ok := range map[string]bool{
		"": true, "https://oauth2.googleapis.com/token": true, "https://accounts.google.com/o/oauth2/token": true,
		"https://oauth2.example.test/token": false, "http://127.0.0.1:9/token": false, "https://oauth2.googleapis.com/token/": false,
		"https://oauth2.googleapis.com.example.test/token": false, "http://oauth2.googleapis.com/token": false,
	} {
		var raw map[string]any
		_ = json.Unmarshal(srv.ServiceAccount("p", true), &raw)
		raw["token_uri"] = uri
		b, _ := json.Marshal(raw)
		sa, err := readServiceAccount(b)
		if (err == nil) != ok {
			t.Errorf("token_uri %q: err = %v, want accepted %v", uri, err, ok)
		}
		if uri == "" && err == nil && sa.TokenURI != defaultTokenURI {
			t.Errorf("no token_uri: TokenURI = %q, want %q", sa.TokenURI, defaultTokenURI)
		}
	}
}

// Under a test, a fake token endpoint is taken, but the assertion still never crosses a network
// in the clear.
func TestTokenURIPlainHTTPOnlyLocal(t *testing.T) {
	srv := geminitest.New(t)
	for uri, ok := range map[string]bool{
		"https://oauth2.example.test/token": true, "http://127.0.0.1:9/token": true, "http://localhost:9/token": true,
		"http://[::1]:9/token": true, "http://collector.example.test/token": false, "ftp://127.0.0.1/token": false,
	} {
		var raw map[string]any
		_ = json.Unmarshal(srv.ServiceAccount("p", true), &raw)
		raw["token_uri"] = uri
		b, _ := json.Marshal(raw)
		if _, err := readServiceAccount(b); (err == nil) != ok {
			t.Errorf("token_uri %s: err = %v, want accepted %v", uri, err, ok)
		}
	}
}

// A token endpoint that stops answering fails the fetch instead of holding the account.
func TestTokenFetchTimesOut(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	hung := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hung:
		case <-r.Context().Done():
		}
	}))
	defer ts.Close()
	defer close(hung)
	old := tokenClient.Timeout
	if old <= 0 || old > time.Minute {
		t.Fatalf("token client timeout %v, want a bound of at most a minute", old)
	}
	tokenClient.Timeout = 200 * time.Millisecond
	defer func() { tokenClient.Timeout = old }()
	srv := geminitest.New(t)
	var raw map[string]any
	_ = json.Unmarshal(srv.ServiceAccount("p", true), &raw)
	raw["token_uri"] = ts.URL + "/token"
	b, _ := json.Marshal(raw)
	sa, err := readServiceAccount(b)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := tokens.get(context.Background(), sa); done <- err }()
	select {
	case err := <-done:
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeUnavailable || !e.Retryable {
			t.Errorf("err = %v, want a retryable unavailable error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the token fetch waits for a token endpoint that never answers")
	}
}

// serviceAccountAt returns a service account whose token endpoint is the server's /token.
func serviceAccountAt(t *testing.T, tokenURL string) *serviceAccount {
	t.Helper()
	var raw map[string]any
	_ = json.Unmarshal(geminitest.New(t).ServiceAccount("p", true), &raw)
	raw["token_uri"] = tokenURL
	b, _ := json.Marshal(raw)
	sa, err := readServiceAccount(b)
	if err != nil {
		t.Fatal(err)
	}
	return sa
}

// The token endpoint's reply decides the error: a reply that breaks off or cannot be read says
// nothing about the key, so it is an unavailable error, not a refusal of the key; a complete 200
// without a token is an upstream error; a refusal the endpoint wrote stays an auth error. No
// request has reached the model in any of them, so another account may serve the request.
func TestTokenFetchErrors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		handler   http.HandlerFunc
		code      string
		retryable bool
	}{
		{"200 whose body breaks off", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", "200")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"ya29.partial`)) // 200 bytes promised, the connection ends
		}, llm.CodeUnavailable, true},
		{"400 whose body breaks off", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "200")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_`))
		}, llm.CodeUnavailable, true},
		{"200 with a body that is not JSON", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>captive portal</html>"))
		}, llm.CodeUpstream, true},
		{"200 with no access token", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"expires_in":3599}`))
		}, llm.CodeUpstream, true},
		{"400 invalid_grant", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`))
		}, llm.CodeAuth, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(tc.handler)
			defer ts.Close()
			_, _, err := fetchToken(context.Background(), serviceAccountAt(t, ts.URL+"/token"))
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != tc.code || e.Retryable != tc.retryable {
				t.Errorf("err = %v, want code %s with retryable %v", err, tc.code, tc.retryable)
			}
		})
	}
}

// A Gemini client's countTokens body that wraps a whole request reaches Vertex AI as that
// request's contents, since Vertex AI takes no generateContentRequest.
func TestVertexCountUnwraps(t *testing.T) {
	tokens = &tokenCache{entries: map[string]*tokenEntry{}}
	srv := geminitest.New(t)
	srv.Generate = func(w http.ResponseWriter, r *http.Request, _ geminitest.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}
	call := vertexCall(t, srv)
	call.Request.Native.Body = []byte(`{"generateContentRequest":{"model":"models/gemini-x","contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)
	_ = countTokens(context.Background(), call, nil)
	reqs := srv.Requests()
	if len(reqs) != 1 || reqs[0].Kind != "vertex" || reqs[0].Method != "countTokens" {
		t.Fatalf("requests = %+v", reqs)
	}
	if got := string(reqs[0].Body); got != `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}` {
		t.Errorf("Vertex got %s", got)
	}
}
