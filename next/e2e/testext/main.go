// Command testext is a provider extension for core's lifecycle tests and the web panel's page
// tests. It answers with the version in its package's manifest and its process ID, so a test can
// tell which generation and which process served a request. It calls no upstream.
//
// The requested model picks the behaviour:
//
//	"gated"         sends its first text, then waits until GET <settings.gate> answers, then finishes
//	"crash"         sends its first text, then exits with status 2
//	"rate-limited"  fails at once with a retry-safe 429 that asks for 5 minutes (a model cooldown)
//	"server-error"  fails at once with a 500 that blocks nothing (a failed request, an error log)
//	other           answers at once
//
// It signs in with three login methods, one of each kind:
//
//	"browser"  shows a link and waits for the pasted redirect URL: code=ok signs in, code=bad is
//	           refused with a fixed reason, any other address is asked for again
//	"device"   shows the user code TEST-1234 and signs in 3 seconds later
//	"api_key"  signs in with the key it is given, and refuses the key "bad"
//
// A login's accounts serve the models fake-model, rate-limited and server-error; config accounts
// keep serving whatever their config names, or any model. Every account answers check, and quota
// with a fixed plan, and a signed-in account renews its credential.
//
// A file named crash-on-start in the package folder makes it exit before its handshake. Each
// start appends the process ID to the file starts in the package folder.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

type settings struct {
	Gate string `json:"gate"`
}

// The models that fail on purpose.
const (
	modelRateLimited = "rate-limited"
	modelServerError = "server-error"
)

// rateLimitRetryAfter is how long the rate-limited model asks to be left alone.
const rateLimitRetryAfter = 5 * time.Minute

// The login methods.
const (
	methodBrowser = "browser"
	methodDevice  = "device"
	methodAPIKey  = "api_key"
	// methodSettings signs in with a grant that carries the account settings in the input "settings"
	// (the grant-settings e2e test); the account gets a made-up key.
	methodSettings = "settings"
)

const (
	userCode      = "TEST-1234"
	deviceDelay   = 3 * time.Second
	refusedReason = "the test provider refused this sign-in"
	refusedKey    = "bad"
)

var loginMethods = []protocol.LoginMethod{
	{Method: methodBrowser, Kind: protocol.LoginBrowser, Label: "Test browser"},
	{Method: methodDevice, Kind: protocol.LoginDevice, Label: "Test device"},
	{Method: methodSettings, Kind: protocol.LoginAPIKey, Label: "Test settings"},
	{Method: methodAPIKey, Kind: protocol.LoginAPIKey, Label: "Test API key",
		Inputs: []protocol.LoginInput{{Name: "api_key", Label: "API key", Secret: true}}},
}

// Fixed quota: a plan and two windows, reset at known times in the future.
const (
	quotaPlan         = "Test plan"
	fiveHourResetsAt  = 2_000_000_000
	weeklyResetsAt    = 2_000_086_400
	fiveHourPercent   = 42
	weeklyPercent     = 7
	fiveHourWindowSec = 5 * 3600
	weeklyWindowSec   = 7 * 24 * 3600
)

// loginMarker is the data of every credential a login made, which tells those accounts from the
// config's.
const loginMarker = `{"login":1}`

func main() {
	// Core starts the program in its package folder.
	if _, err := os.Stat("crash-on-start"); err == nil {
		fmt.Fprintln(os.Stderr, "testext: crash-on-start is set")
		os.Exit(3)
	}
	var m struct {
		Version string `json:"version"`
	}
	if b, err := os.ReadFile("pluxy-extension.json"); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	f, err := os.OpenFile("starts", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, os.Getpid())
		f.Close()
	}
	reply := fmt.Sprintf("v=%s pid=%d", m.Version, os.Getpid())
	err = sdk.Serve(sdk.Extension{
		ID:      os.Getenv(protocol.EnvExtensionID),
		Version: m.Version,
		Generate: func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
			return generate(ctx, call, out, reply)
		},
		LoginMethods: loginMethods,
		Login:        login,
		Refresh:      refresh,
		Models:       models,
		Check:        check,
		Quota:        quota,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "testext:", err)
		os.Exit(1)
	}
}

func generate(ctx context.Context, call sdk.Call, out *sdk.Stream, reply string) error {
	if call.Request.Model == "account-settings" {
		reply = string(call.Account.Settings)
	}
	var s settings
	_ = json.Unmarshal(call.Account.Settings, &s)
	switch call.Request.Model {
	case modelRateLimited:
		return &llm.Error{Code: llm.CodeRateLimit, Message: "the test provider rate limited this model", Status: http.StatusTooManyRequests,
			Retryable: true, Scope: llm.ScopeModel, RetryAfterMS: rateLimitRetryAfter.Milliseconds()}
	case modelServerError:
		return &llm.Error{Code: llm.CodeUpstream, Message: "the test provider failed this request", Status: http.StatusInternalServerError,
			Scope: llm.ScopeRequest}
	}
	if err := out.Start(llm.ResponseInfo{ID: "testext-" + strconv.Itoa(os.Getpid()), Model: call.Request.Model}); err != nil {
		return err
	}
	item, err := out.StartItem(llm.ItemStart{Kind: llm.ItemMessage})
	if err != nil {
		return err
	}
	if err := out.Text(item, reply); err != nil {
		return err
	}
	switch call.Request.Model {
	case "crash":
		os.Exit(2)
	case "gated":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Gate, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &llm.Error{Code: llm.CodeUpstream, Message: "gate: " + err.Error()}
		}
		resp.Body.Close()
	}
	if err := out.Text(item, " done"); err != nil {
		return err
	}
	if err := out.EndItem(item); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop, RawReason: "stop"}, nil)
}

// login signs in with the method the call names.
func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	switch call.Method {
	case methodBrowser:
		for prompt := "Open the link, sign in and paste the address the browser ends on."; ; {
			if err := s.Prompt(protocol.Prompt{Kind: protocol.PromptBrowser, URL: "https://login.example.test/authorize", ExpiresIn: 900,
				Message: prompt, Inputs: []protocol.LoginInput{{Name: "redirect_url", Label: "Address the browser ended on"}}}); err != nil {
				return nil, err
			}
			in, err := s.Input(ctx)
			if err != nil {
				return nil, err
			}
			var code string
			if u, err := url.Parse(strings.TrimSpace(in["redirect_url"])); err == nil {
				code = u.Query().Get("code")
			}
			switch code {
			case "ok":
				return grant(call.Account.ID, oauthCredential("fake-refresh-token")), nil
			case "bad":
				return nil, &llm.Error{Code: llm.CodeAuth, Message: refusedReason}
			}
			prompt = "That address holds no sign-in code; sign in and paste the whole address the browser ended on."
		}
	case methodDevice:
		if err := s.Prompt(protocol.Prompt{Kind: protocol.PromptDevice, UserCode: userCode, VerificationURI: "https://login.example.test/device",
			ExpiresIn: 900, Message: "Open the link and enter the code."}); err != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(deviceDelay):
		}
		return grant(call.Account.ID, oauthCredential("fake-refresh-token")), nil
	case methodSettings:
		var g protocol.Grant
		raw, _ := json.Marshal(map[string]any{"credential": map[string]string{"kind": "api_key", "api_key": "testext-made-up-key"}, "settings": json.RawMessage(call.Input["settings"])})
		err := json.Unmarshal(raw, &g)
		return &g, err
	case methodAPIKey:
		key := call.Input["api_key"]
		if key == refusedKey {
			return nil, &llm.Error{Code: llm.CodeAuth, Message: "the test provider refused this key", Status: http.StatusUnauthorized, Scope: llm.ScopeCredential}
		}
		return grant(call.Account.ID, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: key, Data: json.RawMessage(loginMarker)}), nil
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "unknown login method " + call.Method, Scope: llm.ScopeRequest}
}

func grant(account string, cred *protocol.Credential) *protocol.Grant {
	return &protocol.Grant{Credential: cred, Label: "Test account", Identity: map[string]string{"subject": account}}
}

// oauthCredential is a new OAuth credential that expires in an hour.
func oauthCredential(refreshToken string) *protocol.Credential {
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "fake-access-token", RefreshToken: refreshToken,
		ExpiresAt: time.Now().Add(time.Hour).Unix(), Data: json.RawMessage(loginMarker)}
}

// refresh renews a signed-in account's credential, keeping its refresh token.
func refresh(_ context.Context, call sdk.RefreshCall) (*protocol.Credential, error) {
	c := call.Account.Credential
	if c == nil || c.Kind != protocol.CredentialOAuth {
		return nil, &llm.Error{Code: llm.CodeUnsupported, Message: "only a signed-in account's credential is renewed", Scope: llm.ScopeCredential}
	}
	return oauthCredential(c.RefreshToken), nil
}

// models lists the models of an account a login made; any other account keeps its config's.
func models(_ context.Context, call sdk.ModelsCall) (*protocol.ModelList, error) {
	if !fromLogin(call.Account.Credential) {
		return &protocol.ModelList{}, nil
	}
	return &protocol.ModelList{OwnedBy: "test", Models: []protocol.Model{{ID: "fake-model"}, {ID: modelRateLimited}, {ID: modelServerError}}}, nil
}

// fromLogin tells whether a login made the credential.
func fromLogin(c *protocol.Credential) bool {
	var d struct {
		Login int `json:"login"`
	}
	return c != nil && json.Unmarshal(c.Data, &d) == nil && d.Login == 1
}

func check(context.Context, sdk.CheckCall) (*protocol.CheckResult, error) {
	return &protocol.CheckResult{Detail: "the test provider answered"}, nil
}

func quota(context.Context, sdk.QuotaCall) (*protocol.Quota, error) {
	pct := func(v float64) *float64 { return &v }
	return &protocol.Quota{Plan: quotaPlan, PlanEndsAt: 1924992000, Limits: []protocol.QuotaLimit{
		{ID: "five_hour", Label: "5-hour limit", UsedPercent: pct(fiveHourPercent), ResetsAt: fiveHourResetsAt, WindowSeconds: fiveHourWindowSec},
		{ID: "weekly", Label: "Weekly limit", UsedPercent: pct(weeklyPercent), ResetsAt: weeklyResetsAt, WindowSeconds: weeklyWindowSec},
	}}, nil
}
