package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func status(code int, body string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestAPIKeyLoginRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", sdktest.ClosedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", sdktest.LoginServer(t, sdktest.HangUp), llm.CodeUnavailable, false},
		{"500", sdktest.LoginServer(t, status(500, `{}`)), llm.CodeUpstream, false},
		{"503", sdktest.LoginServer(t, status(503, `{}`)), llm.CodeUnavailable, false},
		{"429", sdktest.LoginServer(t, status(429, `{}`)), llm.CodeRateLimit, true},
		{"key refused", sdktest.LoginServer(t, status(400, `{"error":{"status":"INVALID_ARGUMENT","message":"API key not valid"}}`)), llm.CodeAuth, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := apiKeyLogin(context.Background(), settings{BaseURL: c.url}, "key")
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}

func TestServiceAccountLoginRetryOnlyWhenNotSent(t *testing.T) {
	for _, c := range []struct {
		name      string
		url       string
		code      string
		retryable bool
	}{
		{"connection refused", sdktest.ClosedURL(t), llm.CodeUnavailable, true},
		{"hung up after the request", sdktest.LoginServer(t, sdktest.HangUp), llm.CodeUnavailable, false},
		{"503", sdktest.LoginServer(t, status(503, `{}`)), llm.CodeUnavailable, false},
		{"429", sdktest.LoginServer(t, status(429, `{}`)), llm.CodeUnavailable, true},
		{"key refused", sdktest.LoginServer(t, status(400, `{"error":"invalid_grant"}`)), llm.CodeAuth, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			tokens = &tokenCache{entries: map[string]*tokenEntry{}}
			var raw map[string]any
			_ = json.Unmarshal(geminitest.New(t).ServiceAccount("p", true), &raw)
			raw["token_uri"] = c.url + "/token"
			key, _ := json.Marshal(raw)
			set := settings{Vertex: &vertexSettings{Project: "p"}}
			_, err := serviceAccountLogin(context.Background(), set, string(key))
			sdktest.WantError(t, err, c.code, c.retryable)
		})
	}
}

func TestModelRequestTokenFetchStaysRetryable(t *testing.T) {
	for name, url := range map[string]string{
		"hung up": sdktest.LoginServer(t, sdktest.HangUp),
		"503":     sdktest.LoginServer(t, status(503, `{}`)),
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
			sdktest.WantError(t, err, llm.CodeUnavailable, true)
		})
	}
}
