package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

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
// it is a rate limit, with the wait Google names.
func TestRateLimitOrQuota(t *testing.T) {
	for _, c := range []struct {
		name, body string
		code       string
		waitMS     int64
	}{
		{"quota exhausted", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"You have exhausted your capacity.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED","metadata":{"quotaResetDelay":"1h2m3s"}}]}}`, llm.CodeQuota, 3723000},
		{"short rate limit", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2.5s"}]}}`, llm.CodeRateLimit, 2500},
		{"long rate limit", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Rate limited.","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"600s"}]}}`, llm.CodeQuota, 600000},
		{"wait in the message", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Please retry after 42s."}}`, llm.CodeRateLimit, 42000},
		{"no reason", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`, llm.CodeRateLimit, 0},
	} {
		var g googleError
		_ = json.Unmarshal([]byte(c.body), &g)
		e := classify(http.StatusTooManyRequests, &g, []byte(c.body), "m", http.Header{})
		if e.Code != c.code || e.RetryAfterMS != c.waitMS || !e.Retryable || e.Scope != llm.ScopeCredential {
			t.Errorf("%s: %s after %dms (retryable %v, scope %s), want %s after %dms", c.name, e.Code, e.RetryAfterMS, e.Retryable, e.Scope, c.code, c.waitMS)
		}
	}
}

// A declaration's JSON Schema reaches Cloud Code as parameters in the subset it takes: local
// $refs inlined, const as a one-value enum, other keywords removed, property names kept.
func TestCleanTools(t *testing.T) {
	in := `[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,
		"properties":{"pattern":{"type":"string","pattern":"^a"},"where":{"$ref":"#/$defs/place"},"mode":{"const":"fast"}},
		"required":["where"],"$defs":{"place":{"type":"object","properties":{"city":{"type":"string","minLength":1}}}}}}]}]`
	want := `[{"functionDeclarations":[{"name":"f","parameters":{"properties":{"mode":{"enum":["fast"]},"pattern":{"type":"string"},
		"where":{"properties":{"city":{"type":"string"}},"type":"object"}},"required":["where"],"type":"object"}}]}]`
	var got, exp any
	_ = json.Unmarshal(cleanTools(json.RawMessage(in)), &got)
	_ = json.Unmarshal([]byte(want), &exp)
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(exp)
	if string(g) != string(w) {
		t.Errorf("cleaned tools\n %s\nwant\n %s", g, w)
	}
}
