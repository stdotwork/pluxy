package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func apiKeyLoginCall(srv *codextest.Server, key string) (*protocol.Grant, error) {
	return login(context.Background(), sdk.LoginCall{Account: testAccount(srv, nil), Method: "api_key", Input: map[string]string{"api_key": key}}, nil)
}

// loginTo signs in with an API key against an upstream the account does not set: the address
// comes from the sign-in's base_url input, as a person adding a key in the panel or the command
// gives it.
func loginTo(input map[string]string) (*protocol.Grant, error) {
	return login(context.Background(), sdk.LoginCall{Account: protocol.Account{ID: "c"}, Method: "api_key", Input: input}, nil)
}

func asLLMError(t *testing.T, err error) *llm.Error {
	t.Helper()
	var e *llm.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want an *llm.Error", err)
	}
	return e
}

// fakeUpstream serves GET /v1/models with handler and counts the requests it gets.
func fakeUpstream(t *testing.T, handler http.HandlerFunc) (*httptest.Server, func() int) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

const goodList = `{"object":"list","data":[{"id":"m","object":"model"}]}`

// A key the upstream issued is checked with the model list and kept as the account's credential.
func TestAPIKeyLoginKeepsAcceptedKey(t *testing.T) {
	srv := codextest.New(t)
	key := srv.IssueToken("text")
	g, err := apiKeyLoginCall(srv, " "+key+" ")
	if err != nil {
		t.Fatal(err)
	}
	if g.Credential == nil || g.Credential.Kind != protocol.CredentialAPIKey || g.Credential.APIKey != key {
		t.Errorf("credential = %+v, want the trimmed key", g.Credential)
	}
	if g.Settings != nil {
		t.Errorf("settings = %s, want none: the account already sets its base_url", g.Settings)
	}
	n, headers := srv.ModelListRequests()
	if n != 1 || headers[0].Get("Authorization") != "Bearer "+key {
		t.Errorf("model list requests = %d %v, want one with the key", n, headers)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("the sign-in sent a model request: %+v", srv.Requests())
	}
}

// A key the provider refuses is not added, and the error does not repeat the key.
func TestAPIKeyLoginRefusesRefusedKey(t *testing.T) {
	srv := codextest.New(t)
	const key = "sk-test-not-issued"
	_, err := apiKeyLoginCall(srv, key)
	e := asLLMError(t, err)
	if e.Code != llm.CodeAuth || e.Scope != llm.ScopeCredential || !strings.HasSuffix(e.Message, " refused the key") || strings.Contains(e.Message, key) {
		t.Errorf("error = %+v, want auth on the credential saying the provider refused the key", e)
	}
}

// A person adding a key for a compatible provider gives its base_url as an input: the check goes
// there, and the address is returned as the account's setting. Nothing goes to OpenAI.
func TestAPIKeyLoginChecksTheBaseURLInput(t *testing.T) {
	ts, count := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-compat" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, goodList)
	})
	g, err := loginTo(map[string]string{"api_key": "sk-compat", "base_url": ts.URL + "/v1/"})
	if err != nil {
		t.Fatal(err)
	}
	var set map[string]string
	if g.Settings == nil || json.Unmarshal(g.Settings, &set) != nil || set["base_url"] != ts.URL+"/v1" {
		t.Errorf("grant settings = %s, want base_url %s", g.Settings, ts.URL+"/v1")
	}
	if count() != 1 {
		t.Errorf("requests = %d, want 1", count())
	}
	// The provider's refusal names the provider, not OpenAI.
	_, err = loginTo(map[string]string{"api_key": "sk-other", "base_url": ts.URL + "/v1"})
	if e := asLLMError(t, err); e.Code != llm.CodeAuth || strings.Contains(e.Message, "OpenAI") || !strings.Contains(e.Message, strings.TrimPrefix(ts.URL, "http://")) {
		t.Errorf("error = %+v, want the refusal to name the provider's host", e)
	}
}

// A base_url that is not an HTTP or HTTPS address with a host, or carries a query, a fragment or
// user information, is refused as bad input without any request.
func TestAPIKeyLoginRefusesBadBaseURL(t *testing.T) {
	ts, count := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, goodList) })
	for _, base := range []string{"not a url", "ftp://example.test/v1", "/v1", ts.URL + "/v1?x=1", ts.URL + "/v1#frag", "http://user:pw@" + strings.TrimPrefix(ts.URL, "http://") + "/v1"} {
		_, err := loginTo(map[string]string{"api_key": "sk-x", "base_url": base})
		if e := asLLMError(t, err); e.Code != llm.CodeInvalidRequest || e.Retryable {
			t.Errorf("base_url %q: error = %+v, want a non-retryable invalid request", base, e)
		}
	}
	if count() != 0 {
		t.Errorf("requests = %d, want none", count())
	}
}

// A key with a line break or a control character is bad input, said in its own words, and is not
// sent.
func TestAPIKeyLoginRefusesKeyWithControlCharacter(t *testing.T) {
	ts, count := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, goodList) })
	for _, key := range []string{"sk-a\nsk-b", "sk-a\tb", "sk-a\x00"} {
		_, err := loginTo(map[string]string{"api_key": key, "base_url": ts.URL + "/v1"})
		e := asLLMError(t, err)
		if e.Code != llm.CodeInvalidRequest || e.Retryable || !strings.Contains(e.Message, "control character") {
			t.Errorf("key %q: error = %+v, want a non-retryable invalid request about the key's characters", key, e)
		}
	}
	if count() != 0 {
		t.Errorf("requests = %d, want none", count())
	}
}

// Only a 200 with a model list accepts a key. Any other answer that is not the provider
// refusing it leaves the key unadded: an HTML page, a 204, a 200 error body, a redirect to a login
// page (not followed: the login page gets no request).
func TestAPIKeyLoginAcceptsOnlyAModelList(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"html": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "<html>welcome</html>")
		},
		"204":        func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
		"error body": func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"error":{"message":"nope"}}`) },
		"no data":    func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"object":"list"}`) },
		"redirect": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/login" {
				return
			}
			http.Redirect(w, r, "/login", http.StatusFound)
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			ts, count := fakeUpstream(t, h)
			g, err := loginTo(map[string]string{"api_key": "sk-made-up", "base_url": ts.URL + "/v1"})
			if g != nil || err == nil {
				t.Fatalf("grant = %+v, err = %v, want the key refused", g, err)
			}
			if e := asLLMError(t, err); e.Retryable {
				t.Errorf("error = %+v, want it not retryable", e)
			}
			if count() != 1 {
				t.Errorf("requests = %d, want exactly the one check (a redirect is not followed)", count())
			}
		})
	}
}

// A 404 or 405 whose body says the key is bad or the account closed refuses the key, as the status
// 401 would.
func TestAPIKeyLoginReadsTheBodyOf404(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"Incorrect API key provided: sk-ma****","type":"invalid_request_error","code":"invalid_api_key"}}`,
		`{"error":{"message":"Your account has been deactivated.","code":"account_deactivated"}}`,
	} {
		ts, _ := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, body)
		})
		_, err := loginTo(map[string]string{"api_key": "sk-made-up", "base_url": ts.URL + "/v1"})
		if e := asLLMError(t, err); e.Code != llm.CodeAuth || !strings.HasSuffix(e.Message, " refused the key") {
			t.Errorf("body %s: error = %+v, want the key refused", body, e)
		}
	}
}

// Another host with no model list (a 404 or 405 that does not refuse the key) cannot check a key
// without running a model: the key is kept, and its base_url comes back as the account's setting.
func TestAPIKeyLoginKeepsKeyWhereOtherHostHasNoModelList(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		ts, _ := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
		g, err := loginTo(map[string]string{"api_key": "any-key", "base_url": ts.URL + "/v1"})
		var set map[string]string
		if err != nil || g == nil || g.Credential.APIKey != "any-key" || json.Unmarshal(g.Settings, &set) != nil || set["base_url"] != ts.URL+"/v1" {
			t.Errorf("status %d: grant = %+v, err = %v, want the key kept with its base_url", status, g, err)
		}
	}
}

// With no base_url the check goes to OpenAI's own address, which always has a model list: a 404
// or 405 there is an error, never a kept key. The test replaces the transport, so nothing leaves
// the machine.
func TestAPIKeyLoginDefaultAddressNeverKeepsKeyOn404(t *testing.T) {
	var hosts []string
	old := loginClient.Transport
	t.Cleanup(func() { loginClient.Transport = old })
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		loginClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			hosts = append(hosts, r.URL.Scheme+"://"+r.URL.Host+r.URL.Path)
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"not found"}}`)), Request: r}, nil
		})
		g, err := loginTo(map[string]string{"api_key": "sk-made-up"})
		if g != nil || err == nil {
			t.Errorf("status %d: grant = %+v, err = %v, want an error", status, g, err)
		}
	}
	for _, h := range hosts {
		if h != "https://api.openai.com/v1/models" {
			t.Errorf("the check went to %s, want OpenAI's default model list", h)
		}
	}
	if len(hosts) != 2 {
		t.Errorf("requests = %d, want 2", len(hosts))
	}
}

// An upstream that cannot be reached is reported as that, not as a wrong key, naming where it
// looked, and the account is not added.
func TestAPIKeyLoginUnreachable(t *testing.T) {
	srv := codextest.New(t)
	key := srv.IssueToken("text")
	srv.Close()
	g, err := apiKeyLoginCall(srv, key)
	if g != nil {
		t.Errorf("grant = %+v, want none", g)
	}
	e := asLLMError(t, err)
	if e.Code != llm.CodeUnavailable || e.Scope != llm.ScopeEndpoint || strings.Contains(e.Message, "OpenAI") || !strings.Contains(e.Message, "the provider at 127.0.0.1:") {
		t.Errorf("error = %+v, want unavailable on the endpoint, naming the provider's host", e)
	}
}

// An upstream failure is reported without the key, bounded, and is not retried.
func TestAPIKeyLoginUpstreamFailureIsBounded(t *testing.T) {
	const key = "sk-test-echoed-0123456789abcdef"
	for name, body := range map[string]string{
		"echo":    `{"error":{"message":"bad gateway for ` + key + `"}}`,
		"masked":  `{"error":{"message":"bad gateway for sk-****` + key[len(key)-6:] + `"}}`,
		"huge":    `{"error":{"message":"` + strings.Repeat("x ", 450000) + `"}}`,
		"rawecho": `{"error":{"message":"bad"},"echo":"` + key + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ts, _ := fakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, body)
			})
			_, err := loginTo(map[string]string{"api_key": key, "base_url": ts.URL + "/v1"})
			e := asLLMError(t, err)
			if e.Code == llm.CodeAuth || e.Retryable || strings.Contains(e.Message, key) || strings.Contains(e.Message, key[len(key)-6:]) {
				t.Errorf("error = %+v, want a non-auth, non-retryable failure without the key", e)
			}
			if n := len([]rune(e.Message)); n > sdk.MaxUpstreamText+50 {
				t.Errorf("message is %d characters", n)
			}
			if len(e.Raw) != 0 {
				t.Errorf("the raw body came through: %.100s", e.Raw)
			}
		})
	}
}
