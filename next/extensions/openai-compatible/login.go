package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

var loginMethods = []protocol.LoginMethod{
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "OpenAI-compatible API key", Inputs: []protocol.LoginInput{
		{Name: "base_url", Label: "Service address (the API URL, including /v1 for most services)"},
		{Name: "api_key", Label: "API key", Secret: true},
		{Name: "wire_api", Label: "How pluxy talks to it. Leave blank for most services (uses chat). Other values: responses, messages, gemini.", Optional: true},
		{Name: "quota", Label: "How pluxy checks your remaining balance. Leave blank for most services (detected from the address). Values: openrouter, deepseek, none.", Optional: true},
	}},
}

func login(ctx context.Context, call sdk.LoginCall, _ *sdk.LoginSession) (*protocol.Grant, error) {
	if call.Method != "api_key" {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", call.Method), Scope: llm.ScopeRequest}
	}
	var set settings
	if len(call.Account.Settings) > 0 && string(call.Account.Settings) != "null" {
		if json.Unmarshal(call.Account.Settings, &set) != nil {
			return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid JSON", Scope: llm.ScopeCredential}
		}
	}
	var explicit map[string]json.RawMessage
	_ = json.Unmarshal(call.Account.Settings, &explicit)
	for _, name := range []string{"base_url", "wire_api", "quota"} {
		if raw, ok := explicit[name]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: name + " must be a string, not null", Scope: llm.ScopeRequest}
		}
	}
	// Explicit account settings win over login inputs, just as they win over grant defaults.
	if set.BaseURL == "" {
		set.BaseURL = call.Input["base_url"]
	}
	if set.WireAPI == "" {
		set.WireAPI = call.Input["wire_api"]
	}
	if set.Quota == "" {
		set.Quota = call.Input["quota"]
	}
	set.BaseURL = strings.TrimRight(strings.TrimSpace(set.BaseURL), "/")
	set.WireAPI = strings.TrimSpace(set.WireAPI)
	set.Quota = strings.TrimSpace(set.Quota)
	if set.WireAPI == "" {
		set.WireAPI = wireChat
	}
	bad := func(message string) error {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: message, Scope: llm.ScopeRequest}
	}
	if set.BaseURL == "" {
		return nil, bad("enter a base_url including its API path")
	}
	u, err := url.Parse(set.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, bad("base_url must be an HTTP or HTTPS URL without user information, a query or a fragment")
	}
	wire, ok := wires[set.WireAPI]
	if !ok {
		return nil, bad("wire_api must be chat, responses, messages or gemini")
	}
	switch set.Quota {
	case "", quotaOpenRouter, quotaDeepSeek, quotaNone:
	default:
		return nil, bad("quota must be openrouter, deepseek or none")
	}
	key := strings.TrimSpace(call.Input["api_key"])
	if key == "" {
		return nil, bad("enter an API key")
	}
	credential := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}
	redact := protocol.NewRedactor(credential)
	clean := func(e *llm.Error) error {
		e = redact.Event(llm.Event{Type: llm.EventError, Error: e}).Error
		e.Message = sdk.LoginText(e.Message)
		e.ProviderCode = sdk.LoginCode(e.ProviderCode)
		// A login needs the refusal reason, not a raw body that may echo credentials.
		e.Raw = nil
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.BaseURL+"/models", nil)
	if err != nil {
		return nil, bad("bad base_url")
	}
	setKey(req, wire, key, nil)
	req.Header.Set("Accept", "application/json")
	req, sent := sdk.TrackSend(req)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, clean(&llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the model list: " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint})
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := upstreamError(resp, "")
		if e.Status >= 500 || e.Code == llm.CodeAuth || e.Code == llm.CodePermission {
			e.Retryable = false
		}
		return nil, clean(e)
	}
	grantSettings := map[string]string{"base_url": set.BaseURL, "wire_api": set.WireAPI}
	if set.Quota != "" {
		grantSettings["quota"] = set.Quota
	}
	defaults, _ := json.Marshal(grantSettings)
	return &protocol.Grant{Credential: credential, Settings: defaults}, nil
}
