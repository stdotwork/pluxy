package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/antigravity/agtest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// An account with no Cloud Code project yet is onboarded on its default tier, and the login
// waits for onboarding to finish.
func TestFindProjectOnboards(t *testing.T) {
	srv := agtest.New(t)
	srv.NoProject, srv.OnboardPolls = true, 1
	access, _ := srv.Grant()
	set, _ := readSettings(srv.Settings())
	p, err := findProject(context.Background(), set, access)
	if err != nil || p != agtest.Project {
		t.Fatalf("project %q, err %v; want %q", p, err, agtest.Project)
	}
	var onboard struct {
		TierID string `json:"tier_id"`
	}
	var polls int
	for _, r := range srv.Requests() {
		if r.Path == "/v1internal:onboardUser" {
			polls++
			_ = json.Unmarshal(r.Body, &onboard)
		}
	}
	if polls != 2 || onboard.TierID != "standard-tier" {
		t.Errorf("onboardUser polled %d times with tier %q, want 2 polls on the default tier standard-tier", polls, onboard.TierID)
	}
}

// A renewal keeps the refresh token Google does not send again, and the project and client
// secret the credential holds.
func TestRefreshKeepsLogin(t *testing.T) {
	srv := agtest.New(t)
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{ProjectID: "kept-project", ClientSecret: agtest.ClientSecret})
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	got := readData(c)
	if c.AccessToken == access || c.RefreshToken != rt || got.ProjectID != "kept-project" || got.ClientSecret != agtest.ClientSecret {
		t.Errorf("renewed credential %+v with data %+v; want a new access token, the old refresh token, project and secret kept", c, got)
	}
	if c.RefreshAt == 0 || c.ExpiresAt-c.RefreshAt != int64(refreshLead.Seconds()) {
		t.Errorf("refresh_at %d, expires_at %d; want renewal %v before expiry", c.RefreshAt, c.ExpiresAt, refreshLead)
	}
}

// A credential without the client secret cannot be renewed: the user has to log in again.
func TestRefreshNeedsClientSecret(t *testing.T) {
	srv := agtest.New(t)
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{ProjectID: agtest.Project})
	_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	var e *llm.Error
	if !errors.As(err, &e) || e.Code != llm.CodeAuth || !strings.Contains(e.Message, "client secret") {
		t.Fatalf("err = %v, want an auth error naming the missing client secret", err)
	}
}

// A 429 is a used-up quota when Google says so, or when the wait it asks for is long; otherwise
// it is a rate limit, with the wait Google names. It holds back only the model, as today's core
// cools only the model that answered, except a used-up credit balance, which is the account's.
func TestRateLimitOrQuota(t *testing.T) {
	for _, c := range []struct {
		name, body string
		code       string
		waitMS     int64
		scope      string
	}{
		{"quota exhausted", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"You have exhausted your capacity.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"1h2m3s"}}]}}`, llm.CodeQuota, 3723000, llm.ScopeModel},
		{"short rate limit", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2.5s"}]}}`, llm.CodeRateLimit, 2500, llm.ScopeModel},
		{"long rate limit", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"600s"}]}}`, llm.CodeQuota, 600000, llm.ScopeModel},
		{"wait in the message", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Please retry after 42s."}}`, llm.CodeRateLimit, 42000, llm.ScopeModel},
		{"no reason", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`, llm.CodeRateLimit, 0, llm.ScopeModel},
		{"credits used up", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Insufficient credits.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`, llm.CodeQuota, 0, llm.ScopeCredential},
	} {
		var g googleError
		_ = json.Unmarshal([]byte(c.body), &g)
		e := classify(http.StatusTooManyRequests, &g, []byte(c.body), "m", http.Header{})
		if e.Code != c.code || e.RetryAfterMS != c.waitMS || !e.Retryable || e.Scope != c.scope {
			t.Errorf("%s: %s after %dms (retryable %v, scope %s), want %s after %dms on the %s", c.name, e.Code, e.RetryAfterMS, e.Retryable, e.Scope, c.code, c.waitMS, c.scope)
		}
	}
}

// A declaration's JSON Schema reaches Cloud Code as parameters in the subset it takes, as today's
// core sends it: local $refs inlined, constraints and enums as description hints, a type list as
// a nullable type, other keywords removed, property names kept. The expected output is today's
// core's for the same schema (corpus case tool-schema-forms).
func TestCleanTools(t *testing.T) {
	in := `[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,
		"properties":{"pattern":{"type":"string","pattern":"^a"},"where":{"$ref":"#/$defs/place"},"mode":{"const":"fast"},"note":{"type":["string","null"],"description":"Optional note"}},
		"required":["where"],"$defs":{"place":{"type":"object","properties":{"city":{"type":"string","minLength":1}}}}}}]}]`
	want := `[{"functionDeclarations":[{"name":"f","parameters":{"description":"No extra properties allowed","properties":{"mode":{"description":"Allowed: fast"},
		"note":{"description":"Optional note ((nullable))","nullable":true,"type":"string"},"pattern":{"description":"pattern: ^a","type":"string"},
		"where":{"properties":{"city":{"description":"minLength: 1","type":"string"}},"type":"object"}},"required":["where"],"type":"object"}}]}]`
	var got, exp any
	_ = json.Unmarshal(cleanTools(json.RawMessage(in), false, false), &got)
	_ = json.Unmarshal([]byte(want), &exp)
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(exp)
	if string(g) != string(w) {
		t.Errorf("cleaned tools\n %s\nwant\n %s", g, w)
	}
}

// A Google error that echoes a token or the client secret issued or used after the token
// exchange does not carry it out: core does not know the new credential yet, so it cannot redact
// it. Covers the login's user-info and project calls and renewal's project discovery.
func TestErrorsHideNewTokens(t *testing.T) {
	var seen string // the token the echoing call was sent
	echo := func(path string) func(http.ResponseWriter, *http.Request) bool {
		return func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != path {
				return false
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			seen = tok
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 400, "status": "INVALID_ARGUMENT",
				"message": "bad request for token " + tok + " with secret " + agtest.ClientSecret}})
			return true
		}
	}
	check := func(name string, err error, secrets ...string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: error %q holds a secret", name, err)
			}
		}
		if !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
			t.Errorf("%s: error %q lost Google's status", name, err)
		}
	}
	for _, path := range []string{"/oauth2/v2/userinfo", "/v1internal:loadCodeAssist"} {
		srv := agtest.New(t)
		srv.Answer = echo(path)
		access, rt := srv.Grant()
		set, _ := readSettings(srv.Settings())
		_, err := finishLogin(context.Background(), set, agtest.ClientSecret, &tokenResponse{AccessToken: access, RefreshToken: rt, ExpiresIn: 3600})
		check("login "+path, err, access, rt, agtest.ClientSecret)
	}
	srv := agtest.New(t)
	srv.Answer = echo("/v1internal:loadCodeAssist")
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{ClientSecret: agtest.ClientSecret})
	_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	if seen == "" || seen == access {
		t.Fatalf("renewal sent token %q to loadCodeAssist, want a new one", seen)
	}
	check("renewal", err, seen, agtest.ClientSecret)
}

// An error event after the reply began is not retry-safe: the model already ran. One that comes
// first keeps the retryability its status gives.
func TestStreamErrorAfterOutput(t *testing.T) {
	text := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5}}}` + "\n\n"
	limit := `data: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited."}}` + "\n\n"
	for _, c := range []struct {
		name, body string
		retryable  bool
	}{{"first", limit, true}, {"after output", text + limit, false}} {
		err := relayStream(context.Background(), strings.NewReader(c.body), discard{})
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeRateLimit || e.Retryable != c.retryable {
			t.Errorf("%s: err %v (retryable %v), want a rate limit with retryable %v", c.name, err, e != nil && e.Retryable, c.retryable)
		}
	}
}

type discard struct{}

func (discard) Send(llm.Event) error { return nil }

// The goroutine that forwards login input ends once the login's context is cancelled, even with
// input waiting to be read.
func TestForwardInputsStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input := func(ctx context.Context) (map[string]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return map[string]string{"redirect_url": "x"}, nil
	}
	inputs, _ := forwardInputs(ctx, input)
	<-inputs
	cancel()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-inputs:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("the input forwarder still runs after the login was cancelled")
		}
	}
}
