// Command codex is the pluxy provider extension for OpenAI's Codex models. It serves accounts
// of two kinds: an OpenAI API key, and a ChatGPT subscription signed in through OpenAI's login
// (in a browser, or with a device code), whose tokens it renews. Both speak OpenAI Responses over
// HTTP with server-sent events; a subscription calls the ChatGPT backend's Codex endpoint.
//
// Account settings, all optional:
//
//	{"base_url": "https://api.openai.com/v1", "codex_identity": true, "image_generation_tool": true,
//	 "callback_listen": true, "websockets": false, "alpha_search": false}
//
// base_url is where requests go ({base_url}/responses); the default is
// https://api.openai.com/v1 for an API key and https://chatgpt.com/backend-api/codex for a
// subscription. codex_identity (default true) sends the User-Agent and originator headers of the
// Codex terminal client in place of the client's own. image_generation_tool (default true) offers
// the model the hosted image_generation tool (never to a free plan or a -spark model); both
// follow what today's core sends.
// Tests move OpenAI's login to a fake with auth_base_url (sdk.TestLoginHost); in a normal run it is
// ignored. callback_listen (default true) lets
// the browser login listen for its redirect on localhost:1455; off, the user pastes the address
// the browser ended on, as they must when core runs on another machine. websockets (default
// false, as in today's core) holds sessions for WebSocket clients: their turns go over the
// upstream's Responses WebSocket ({base_url}/responses with ws or wss), one upstream socket per
// client socket, so a turn can continue the previous response with previous_response_id.
// alpha_search (default false, as in today's core) lets an API key account serve Codex's Alpha
// Search ({base_url}/alpha/search), which not every upstream has; a subscription always serves it.
//
// Every request is sent in the shape the Codex endpoint takes: streamed, not stored, encrypted
// reasoning included, sampling settings removed (see shape). Requests in Chat Completions,
// Anthropic Messages and Gemini formats are translated to Responses; a Messages client keeps
// the encrypted reasoning in its thinking blocks' signatures and gets it back on the next turn.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const (
	id      = "pluxy/codex"
	version = "0.1.0"
)

func main() {
	err := sdk.Serve(sdk.Extension{
		ID:           id,
		Version:      version,
		Generate:     generate,
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		Session:      openSession,
		Models:       listModels,
		Quota:        quota,
		Endpoint:     endpoint,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex:", err)
		os.Exit(1)
	}
}

// Default endpoints.
const (
	defaultAPIBaseURL          = "https://api.openai.com/v1"
	defaultSubscriptionBaseURL = "https://chatgpt.com/backend-api/codex"
	defaultAuthBaseURL         = "https://auth.openai.com"
)

type settings struct {
	BaseURL             string `json:"base_url"`
	CodexIdentity       *bool  `json:"codex_identity"`
	ImageGenerationTool *bool  `json:"image_generation_tool"`
	AuthBaseURL         string `json:"auth_base_url"`
	// CallbackListen (default true) serves the browser login's redirect on this machine; off,
	// the user always pastes the address the browser ended on.
	CallbackListen *bool `json:"callback_listen"`
	// Websockets (default false) sends a WebSocket client's turns over the upstream's Responses
	// WebSocket, one socket per client socket; off, core sends each turn over HTTP.
	Websockets *bool `json:"websockets"`
	// AlphaSearch (default false) lets an API key account serve Codex's Alpha Search; a
	// subscription always does.
	AlphaSearch bool `json:"alpha_search"`
}

func (s settings) identity() bool  { return s.CodexIdentity == nil || *s.CodexIdentity }
func (s settings) imageTool() bool { return s.ImageGenerationTool == nil || *s.ImageGenerationTool }

func (s settings) authBase() string {
	if v := sdk.TestLoginHost(s.AuthBaseURL); v != "" {
		return v
	}
	return defaultAuthBaseURL
}

func readSettings(raw json.RawMessage) (settings, error) {
	var s settings
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &s); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid: " + err.Error(), Scope: llm.ScopeCredential}
		}
	}
	return s, nil
}

// account is what a generate call needs from the account's credential.
type account struct {
	token        string // bearer token: the API key, or the subscription's access token
	subscription bool
	accountID    string // ChatGPT account ID, for a subscription
	planType     string // ChatGPT plan, for a subscription, when known
}

// credentialData is what a subscription credential keeps in its data. Core treats every string
// in a credential as secret and removes it from replies, so only values that never appear in a
// reply belong here; the plan type ("plus") is read from the ID token instead.
type credentialData struct {
	IDToken   string `json:"id_token,omitempty"`
	AccountID string `json:"account_id,omitempty"`
}

func accountOf(c *protocol.Credential) (account, error) {
	if c != nil {
		switch {
		case c.Kind == protocol.CredentialAPIKey && c.APIKey != "":
			return account{token: c.APIKey}, nil
		case c.Kind == protocol.CredentialOAuth && c.AccessToken != "":
			a := account{token: c.AccessToken, subscription: true}
			var d credentialData
			if len(c.Data) > 0 {
				_ = json.Unmarshal(c.Data, &d)
			}
			a.accountID = d.AccountID
			if claims, ok := parseIDToken(d.IDToken); ok {
				if a.accountID == "" {
					a.accountID = claims.accountID
				}
				a.planType = strings.TrimSpace(claims.planType)
			}
			// An ID token with no plan, or one that does not parse, is the free plan, as today's
			// core reads it (core/internal/watcher/synthesizer/file.go) and as listModels does.
			// With no ID token there is no plan to read.
			if d.IDToken != "" && a.planType == "" {
				a.planType = "free"
			}
			return a, nil
		}
	}
	return account{}, &llm.Error{Code: llm.CodeAuth, Message: "the account has no API key or access token; sign in again", Scope: llm.ScopeCredential}
}
