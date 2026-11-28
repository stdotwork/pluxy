package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

var loginMethods = []protocol.LoginMethod{
	{Method: "api_key", Kind: protocol.LoginAPIKey, Label: "API key", Inputs: []protocol.LoginInput{
		{Name: "api_key", Label: "Gemini API key, or a Vertex AI express mode key for a Vertex account", Secret: true},
	}},
	{Method: "service_account", Kind: protocol.LoginAPIKey, Label: "Vertex AI service account", Inputs: []protocol.LoginInput{
		{Name: "service_account", Label: "Service account key (the JSON file from Google Cloud)", Secret: true},
	}},
}

func login(ctx context.Context, call sdk.LoginCall, _ *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	switch call.Method {
	case "api_key":
		return apiKeyLogin(ctx, set, call.Input["api_key"])
	case "service_account":
		return serviceAccountLogin(ctx, set, call.Input["service_account"])
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("unknown login method %q", call.Method), Scope: llm.ScopeRequest}
}

func refused(msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

func apiKeyLogin(ctx context.Context, set settings, key string) (*protocol.Grant, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "enter an API key", Scope: llm.ScopeRequest}
	}
	grant := &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key}}
	if set.Vertex != nil {
		return grant, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.BaseURL+"/v1beta/models?pageSize=1", nil)
	if err != nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("X-Goog-Api-Key", key)
	req, sent := sdk.TrackSend(req)
	resp, err := tokenClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach the Gemini API: " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return grant, nil
	}
	e := upstreamError(resp, "")
	if e.Code == llm.CodeAuth || e.Code == llm.CodePermission || e.Code == llm.CodeAccountClosed {
		return nil, refused("Google refused the key")
	}
	if e.Status >= 500 {
		e.Retryable = false
	}
	r := protocol.NewRedactor(&protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key})
	return nil, r.Event(llm.Event{Type: llm.EventError, Error: e}).Error
}

func serviceAccountLogin(ctx context.Context, set settings, key string) (*protocol.Grant, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: errNoServiceAccount.Error(), Scope: llm.ScopeRequest}
	}
	if !json.Valid([]byte(key)) {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the service account key is not JSON", Scope: llm.ScopeRequest}
	}
	sa, err := readServiceAccount(json.RawMessage(key))
	if err != nil {
		return nil, err
	}
	if set.Vertex == nil || set.Vertex.Project == "" {
		msg := "set the account's vertex.project setting to the Google Cloud project to use"
		if sa.ProjectID != "" {
			msg += fmt.Sprintf(" (the key belongs to %q)", sa.ProjectID)
		}
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: msg, Scope: llm.ScopeRequest}
	}
	ctx, sent := sdk.TrackSendContext(ctx)
	if _, err := tokens.get(ctx, sa); err != nil {
		var e *llm.Error
		if errors.As(err, &e) {
			switch {
			case e.Code == llm.CodeAuth:
				return nil, refused(e.Message)
			case e.Code == llm.CodeUnavailable && e.Status == 0:
				c := *e
				c.Retryable = !sent()
				return nil, &c
			case e.Code == llm.CodeUnavailable && e.Status >= 500:
				c := *e
				c.Retryable = false
				return nil, &c
			}
		}
		return nil, err
	}
	data := map[string]string{"client_email": sa.ClientEmail, "private_key": sa.PrivateKey}
	if sa.TokenURI != defaultTokenURI {
		data["token_uri"] = sa.TokenURI
	}
	b, _ := json.Marshal(data)
	return &protocol.Grant{
		Credential: &protocol.Credential{Kind: CredentialServiceAccount, Data: b},
		Identity:   serviceAccountIdentity(sa.ClientEmail),
	}, nil
}

func serviceAccountIdentity(clientEmail string) map[string]string {
	if clientEmail = strings.TrimSpace(clientEmail); clientEmail == "" {
		return nil
	}
	return map[string]string{"client_email": clientEmail}
}
