package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
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

const (
	defaultAPIBaseURL          = "https://api.openai.com/v1"
	defaultSubscriptionBaseURL = "https://chatgpt.com/backend-api/codex"
	defaultAuthBaseURL         = "https://auth.openai.com"
)

type settings struct {
	BaseURL                         string   `json:"base_url"`
	CodexIdentity                   *bool    `json:"codex_identity"`
	ImageGenerationTool             *bool    `json:"image_generation_tool"`
	AuthBaseURL                     string   `json:"auth_base_url"`
	CallbackListen                  *bool    `json:"callback_listen"`
	Websockets                      *bool    `json:"websockets"`
	AlphaSearch                     bool     `json:"alpha_search"`
	Images                          *bool    `json:"images"`
	SIWCIdentifier                  string   `json:"signin_with_chatgpt_identifier"`
	CodexClientVersion              string   `json:"codex_client_version"`
	ImageGenerationOffModels        []string `json:"image_generation_off_models"`
	PromptCacheRetentionPassthrough bool     `json:"prompt_cache_retention_passthrough"`
}

func (s settings) identity() bool  { return s.CodexIdentity == nil || *s.CodexIdentity }
func (s settings) imageTool() bool { return s.ImageGenerationTool == nil || *s.ImageGenerationTool }
func (s settings) imagesAPI() bool { return s.Images == nil || *s.Images }

func (s settings) imageOff(model string) bool {
	model = strings.ToLower(model)
	for _, p := range s.ImageGenerationOffModels {
		if ok, _ := path.Match(strings.ToLower(p), model); ok {
			return true
		}
	}
	return false
}

var codexVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]*$`)

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
	if strings.ContainsFunc(s.SIWCIdentifier, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid: signin_with_chatgpt_identifier holds a control character", Scope: llm.ScopeCredential}
	}
	if v := s.CodexClientVersion; v != "" && !codexVersionPattern.MatchString(v) {
		return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid: codex_client_version must be a version such as 0.159.0", Scope: llm.ScopeCredential}
	}
	for _, p := range s.ImageGenerationOffModels {
		if _, err := path.Match(p, ""); err != nil {
			return s, &llm.Error{Code: llm.CodeInvalidRequest, Message: "account settings are not valid: image_generation_off_models has a malformed pattern " + fmt.Sprintf("%q", p), Scope: llm.ScopeCredential}
		}
	}
	return s, nil
}

type account struct {
	token        string
	subscription bool
	accountID    string
	planType     string
	siwc         bool
}

type credentialData struct {
	IDToken   string `json:"id_token,omitempty"`
	AccountID string `json:"account_id,omitempty"`
	SIWC      bool   `json:"siwc,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

func accountOf(c *protocol.Credential) (account, error) {
	if c != nil {
		switch {
		case c.Kind == protocol.CredentialAPIKey && c.APIKey != "":
			return account{token: c.APIKey}, nil
		case c.Kind == protocol.CredentialOAuth && c.AccessToken != "":
			var d credentialData
			if len(c.Data) > 0 {
				_ = json.Unmarshal(c.Data, &d)
			}
			if d.SIWC {
				claims, _ := parseIDToken(d.IDToken)
				return account{token: c.AccessToken, siwc: true, planType: claims.planType}, nil
			}
			a := account{token: c.AccessToken, subscription: true}
			a.accountID = d.AccountID
			if claims, ok := parseIDToken(d.IDToken); ok {
				if a.accountID == "" {
					a.accountID = claims.accountID
				}
				a.planType = strings.TrimSpace(claims.planType)
			}
			if d.IDToken != "" && a.planType == "" {
				a.planType = "free"
			}
			return a, nil
		}
	}
	return account{}, &llm.Error{Code: llm.CodeAuth, Message: "the account has no API key or access token; sign in again", Scope: llm.ScopeCredential}
}
