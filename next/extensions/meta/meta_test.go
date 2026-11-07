package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
