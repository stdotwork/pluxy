package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/meta/metatest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// A renewal makes a new API key from the login's device token, keeps the device token, and keeps
// where Meta said the key's API is.
func TestRefreshMintsKey(t *testing.T) {
	srv := metatest.New(t)
	dca, key := srv.Grant()
	set := json.RawMessage(`{"auth_base_url":"` + srv.URL + `","mint_url":"` + srv.URL + `/muse-code/key"}`)
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: set,
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca}}})
	if err != nil {
		t.Fatal(err)
	}
	var d credentialData
	_ = json.Unmarshal(c.Data, &d)
	if c.AccessToken == key || !strings.HasPrefix(c.AccessToken, "metatest-key-") || c.RefreshToken != dca || d.BaseURL != srv.URL+"/v1" {
		t.Errorf("renewed credential %+v (data %+v); want a new key, the device token kept and Meta's base URL", c, d)
	}
	// Without base_url in the settings, the request goes where the key endpoint said.
	s, _ := readSettings(set)
	_, base, err := target(s, c)
	if err != nil || base != srv.URL+"/v1" {
		t.Errorf("target base %q, err %v; want the minted base URL", base, err)
	}
}

// A dca: device token is never sent to Meta's API as a key.
func TestDeviceTokenIsNotAKey(t *testing.T) {
	for _, c := range []*protocol.Credential{
		{Kind: protocol.CredentialOAuth, AccessToken: "dca:abc", RefreshToken: "dca:abc"},
		{Kind: protocol.CredentialAPIKey, APIKey: "dca:abc"},
	} {
		_, _, err := target(settings{}, c)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeAuth {
			t.Errorf("%s credential with a dca: token: err %v, want an auth error", c.Kind, err)
		}
	}
}

// A 429 is a used-up subscription quota when Meta says so, and waits until Meta's reset time; a
// 404 holds the account back from the model for five minutes when Meta names no reset.
func TestUpstreamErrors(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	reset := now.Add(90 * time.Minute).Unix()
	for _, c := range []struct {
		name   string
		status int
		body   string
		code   string
		waitMS int64
	}{
		{"subscription quota", 429, `{"error":{"message":"Subscription quota exhausted","type":"rate_limit_error","code":"rate_limit_exceeded","resets_at":` + jsonInt(reset) + `}}`, llm.CodeQuota, 90 * 60 * 1000},
		{"rate limit", 429, `{"error":{"message":"Slow down","type":"rate_limit_error"}}`, llm.CodeRateLimit, 0},
		{"string error", 429, `{"code":"Too many requests","error":"Rate limit reached for requests"}`, llm.CodeRateLimit, 0},
		{"model not found", 404, `{"error":{"message":"model muse-x not found","type":"not_found_error"}}`, llm.CodeNotFound, notFoundCooldownMS},
	} {
		e := upstreamError(c.status, []byte(c.body), http.Header{}, "muse-x", now)
		if e.Code != c.code || e.RetryAfterMS != c.waitMS || e.Message == "" || strings.HasPrefix(e.Message, "upstream returned") {
			t.Errorf("%s: %s after %dms (%q), want %s after %dms with Meta's message", c.name, e.Code, e.RetryAfterMS, e.Message, c.code, c.waitMS)
		}
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

// A request with tools carries Muse Code's tool namespace once, and web_search tools lose
// search_content_types; a request without tools gets no namespace.
func TestMuseTools(t *testing.T) {
	native := func(body string) llm.Request {
		return llm.Request{Model: "muse-spark-1.3", Native: &llm.Native{Format: llm.FormatOpenAIResponses, Body: json.RawMessage(body)}}
	}
	out, _, err := buildBody(native(`{"input":"hi","tools":[{"type":"web_search","search_content_types":["text"]},{"type":"function","name":"f","parameters":{"type":"object"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		Stream bool             `json:"stream"`
		Tools  []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(out, &b)
	if !b.Stream || len(b.Tools) != 3 || b.Tools[2]["name"] != "muse" || b.Tools[0]["search_content_types"] != nil {
		t.Errorf("body %s: want stream true, search_content_types removed and the muse namespace added last", out)
	}
	again, _, _ := buildBody(native(string(out)))
	if strings.Count(string(again), `"name":"muse"`) != 1 {
		t.Errorf("a request already carrying the namespace got another: %s", again)
	}
	plain, _, _ := buildBody(native(`{"input":"hi"}`))
	if strings.Contains(string(plain), `"name":"muse"`) {
		t.Errorf("a request without tools got the namespace: %s", plain)
	}
}

// An error event in the stream ends the request with the status it names classified like a reply:
// a used-up subscription quota mid-stream is a quota error, as today's core reads it.
func TestStreamErrorEvent(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":429,\"message\":\"Subscription quota exhausted\"}}\n\n"
	err := relayStream(context.Background(), strings.NewReader(body), recorder(func(llm.Event) {}), "muse-spark-1.3")
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeQuota || e.Status != 429 || e.Scope != llm.ScopeCredential {
		t.Fatalf("err = %+v, want a 429 quota error on the credential", err)
	}
}

type recorder func(llm.Event)

func (r recorder) Send(ev llm.Event) error { r(ev); return nil }

// A failure event is classified by the status it names, whether an error event or
// response.failed, and whether the status is a number or a string; once the reply began
// (response.created starts it), it is not retry-safe.
func TestStreamFailures(t *testing.T) {
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n"
	text := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"m\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"m\",\"delta\":\"Hi\"}\n\n"
	for _, c := range []struct {
		name, body string
		code       string
		retryable  bool
	}{
		{"string status first", "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"401\",\"message\":\"Invalid API key\"}}\n\n", llm.CodeAuth, true},
		{"response.failed first", "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"status\":\"failed\",\"error\":{\"code\":429,\"message\":\"Subscription quota exhausted\"}}}\n\n", llm.CodeQuota, true},
		{"response.failed after created", created + "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"r\",\"status\":\"failed\",\"error\":{\"code\":429,\"message\":\"Subscription quota exhausted\"}}}\n\n", llm.CodeQuota, false},
		{"after output", created + text + "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":429,\"message\":\"Subscription quota exhausted\"}}\n\n", llm.CodeQuota, false},
	} {
		err := relayStream(context.Background(), strings.NewReader(c.body), recorder(func(llm.Event) {}), "muse-spark-1.3")
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != c.code || e.Retryable != c.retryable {
			t.Errorf("%s: err %+v, want code %s, retryable %v", c.name, err, c.code, c.retryable)
		}
	}
}

// A login or renewal error that echoes the device token or device code does not carry it out:
// core does not know them yet, so it cannot redact them.
func TestLoginErrorsHideTokens(t *testing.T) {
	srv := metatest.New(t)
	set, _ := readSettings(srv.Settings())
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/muse-code/key" {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request","message":"cannot mint for `+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`"}}`)
		return true
	}
	_, err := mint(context.Background(), set, "dca:secret-device-token")
	if err == nil || strings.Contains(err.Error(), "secret-device-token") {
		t.Errorf("mint error %v holds the device token", err)
	}
	srv2 := metatest.New(t)
	srv2.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oidc/device/token/" {
			return false
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"server_error","error_description":"bad device code `+r.PostForm.Get("device_code")+`"}`)
		return true
	}
	set2, _ := readSettings(srv2.Settings())
	var code string
	_, err = deviceLogin(context.Background(), set2, promptFunc(func(p protocol.Prompt) { code = p.UserCode }))
	if err == nil || code == "" || strings.Contains(err.Error(), "metatest-device-") || !strings.Contains(err.Error(), "server_error") {
		t.Errorf("poll error %v: want server_error without the device code", err)
	}
}

type promptFunc func(protocol.Prompt)

func (f promptFunc) Prompt(p protocol.Prompt) error { f(p); return nil }

// A poll that gets a 5xx stops the login: the poll may have run, so it is not tried again.
func TestPollStopsOnServerFailure(t *testing.T) {
	srv := metatest.New(t)
	var polls atomic.Int32
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oidc/device/token/" {
			return false
		}
		polls.Add(1)
		http.Error(w, "upstream connect error", http.StatusServiceUnavailable)
		return true
	}
	set, _ := readSettings(srv.Settings())
	_, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	wantError(t, err, llm.CodeUnavailable, false)
	if polls.Load() != 1 {
		t.Errorf("%d polls reached Meta, want 1", polls.Load())
	}
}

// An answer with no OAuth error that is not a 5xx leaves the device code good: the poll is tried
// again.
func TestPollSurvivesOddAnswer(t *testing.T) {
	srv := metatest.New(t)
	failed := false
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oidc/device/token/" || failed {
			return false
		}
		failed = true
		http.Error(w, "not here", http.StatusNotFound)
		return true
	}
	set, _ := readSettings(srv.Settings())
	g, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil || g.Credential.AccessToken == "" || !failed {
		t.Fatalf("login after an odd answer: grant %+v, err %v (seen %v)", g, err, failed)
	}
}

// A renewal keeps the API address the credential holds when Meta's key endpoint names none.
func TestRefreshKeepsBaseURL(t *testing.T) {
	srv := metatest.New(t)
	dca, key := srv.Grant()
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/muse-code/key" {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"api_key":"minted-without-url"}`)
		return true
	}
	set := json.RawMessage(`{"auth_base_url":"` + srv.URL + `","mint_url":"` + srv.URL + `/muse-code/key"}`)
	data, _ := json.Marshal(credentialData{BaseURL: "https://regional.example/v1"})
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: set,
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: key, RefreshToken: dca, Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	var got credentialData
	_ = json.Unmarshal(c.Data, &got)
	if got.BaseURL != "https://regional.example/v1" {
		t.Errorf("renewed base_url %q, want the one the credential held", got.BaseURL)
	}
}

// A rate-limited renewal keeps the wait Meta names, so core does not renew again before it.
func TestRenewalRetryAfter(t *testing.T) {
	srv := metatest.New(t)
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/muse-code/key" {
			return false
		}
		w.Header().Set("Retry-After", "900")
		http.Error(w, `{"error":{"type":"rate_limited","message":"slow down"}}`, http.StatusTooManyRequests)
		return true
	}
	set, _ := readSettings(srv.Settings())
	_, err := mint(context.Background(), set, "dca:x")
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeRateLimit || e.RetryAfterMS != 900000 {
		t.Errorf("err %+v, want a rate limit with retry_after_ms 900000", err)
	}
}
