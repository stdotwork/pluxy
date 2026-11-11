package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func apiKeyCall(base, key string) sdk.LoginCall {
	raw, _ := json.Marshal(map[string]string{"base_url": base})
	return sdk.LoginCall{Method: "api_key", Account: protocol.Account{Settings: raw}, Input: map[string]string{"api_key": key}}
}

func TestAPIKeyLoginHost(t *testing.T) {
	for _, wire := range []string{"chat", "messages", "gemini"} {
		t.Run(wire, func(t *testing.T) {
			var requests, keys atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") != "" || r.Header.Get("X-Goog-Api-Key") != "" {
					keys.Add(1)
				}
			}))
			defer other.Close()
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
			}))
			defer redirect.Close()
			call := apiKeyCall(redirect.URL, "made-up-login-key")
			call.Input["wire_api"] = wire
			_, err := login(context.Background(), call, nil)
			if err == nil || requests.Load() != 0 || keys.Load() != 0 {
				t.Fatalf("cross-port redirect: error %v, requests %d, keys %d", err, requests.Load(), keys.Load())
			}
		})
	}
	t.Run("same origin", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if !strings.HasSuffix(r.URL.Path, "/") {
				http.Redirect(w, r, r.URL.Path+"/", http.StatusTemporaryRedirect)
				return
			}
			if r.Header.Get("Authorization") != "Bearer made-up-login-key" {
				t.Error("redirect lost key")
			}
		}))
		defer srv.Close()
		_, err := login(context.Background(), apiKeyCall(srv.URL, "made-up-login-key"), nil)
		if err != nil || calls.Load() != 2 {
			t.Fatalf("same-origin redirect: error %v, requests %d", err, calls.Load())
		}
	})
}

func TestAPIKeyLoginError(t *testing.T) {
	const key = "made-up-login-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "rejected " + key + " " + strings.Repeat("filler ", 500), "type": "invalid_key"}})
	}))
	defer srv.Close()
	_, err := login(context.Background(), apiKeyCall(srv.URL, "  "+key+"  "), nil)
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeAuth {
		t.Fatalf("error = %v", err)
	}
	raw, _ := json.Marshal(e)
	if strings.Contains(string(raw), key) || len([]rune(e.Message)) > sdk.MaxLoginText+1 || !strings.Contains(e.Message, "rejected") {
		t.Fatalf("unsafe error: %s", raw)
	}
	if len(e.Raw) != 0 {
		t.Fatal("login error keeps raw upstream body")
	}
}

func TestAPIKeyLoginLimits(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	_, err := login(context.Background(), apiKeyCall(srv.URL, "   "), nil)
	if err == nil || calls.Load() != 0 {
		t.Fatal("empty key reached upstream")
	}
	_, err = login(context.Background(), apiKeyCall(srv.URL, "made-up-login-key"), nil)
	var e *llm.Error
	if !errors.As(err, &e) || e.Retryable {
		t.Fatalf("5xx login error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = login(ctx, apiKeyCall(srv.URL, "made-up-login-key"), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
