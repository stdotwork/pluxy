package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// All four wire APIs use the same API path convention and their own authentication headers.
func TestAPIKeyLoginWireAPI(t *testing.T) {
	for _, wire := range []string{"chat", "responses", "messages", "gemini"} {
		t.Run(wire, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/custom/v1/models" {
					t.Errorf("path = %s", r.URL.Path)
				}
				header, want := "Authorization", "Bearer made-up-key"
				if wire == "messages" {
					header, want = "X-Api-Key", "made-up-key"
				}
				if wire == "gemini" {
					header, want = "X-Goog-Api-Key", "made-up-key"
				}
				if r.Header.Get(header) != want {
					t.Errorf("missing %s", header)
				}
				if wire == "messages" && r.Header.Get("Anthropic-Version") != anthropicVersion {
					t.Error("missing Anthropic version")
				}
			}))
			defer srv.Close()
			call := sdk.LoginCall{Method: "api_key", Input: map[string]string{"api_key": "made-up-key", "base_url": srv.URL + "/custom/v1/", "wire_api": wire}}
			grant, err := login(context.Background(), call, nil)
			if err != nil {
				t.Fatal(err)
			}
			var settings map[string]string
			json.Unmarshal(grant.Settings, &settings)
			if settings["base_url"] != srv.URL+"/custom/v1" || settings["wire_api"] != wire {
				t.Fatalf("settings: %s", grant.Settings)
			}
		})
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, kind := range []string{"missing", "scheme", "userinfo", "query", "fragment"} {
		t.Run(kind, func(t *testing.T) {
			u, _ := url.Parse(srv.URL + "/v1")
			switch kind {
			case "missing":
				u = &url.URL{}
			case "scheme":
				u.Scheme = "file"
			case "userinfo":
				u.User = url.UserPassword("user", "made-up-password")
			case "query":
				u.RawQuery = "key=value"
			case "fragment":
				u.Fragment = "fragment"
			}
			call := sdk.LoginCall{Method: "api_key", Input: map[string]string{"api_key": "made-up-key", "base_url": u.String()}}
			_, err := login(context.Background(), call, nil)
			if err == nil || calls.Load() != 0 {
				t.Fatalf("unsafe URL: error %v, requests %d", err, calls.Load())
			}
		})
	}

	call := apiKeyCall("https://example.com/v1", "made-up-key")
	call.Input["wire_api"] = "invalid"
	if _, err := login(context.Background(), call, nil); err == nil {
		t.Fatal("accepted invalid wire_api")
	}
}

func TestAPIKeyLoginNullSettings(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, field := range []string{"base_url", "wire_api"} {
		t.Run(field, func(t *testing.T) {
			settings, _ := json.Marshal(map[string]any{field: nil})
			call := sdk.LoginCall{Method: "api_key", Account: protocol.Account{Settings: settings}, Input: map[string]string{"base_url": srv.URL, "wire_api": "chat", "api_key": "made-up-key"}}
			_, err := login(context.Background(), call, nil)
			var e *llm.Error
			if !errors.As(err, &e) || e.Code != llm.CodeInvalidRequest || calls.Load() != 0 {
				t.Fatalf("null %s: error %v, requests %d", field, err, calls.Load())
			}
		})
	}
}
