package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
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

// A renewal keeps the refresh token Google does not send again, and the project the credential
// holds.
func TestRefreshKeepsLogin(t *testing.T) {
	srv := agtest.New(t)
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{ProjectID: "kept-project"})
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	got := readData(c)
	if c.AccessToken == access || c.RefreshToken != rt || got.ProjectID != "kept-project" {
		t.Errorf("renewed credential %+v with data %+v; want a new access token, the old refresh token and the project kept", c, got)
	}
	if c.RefreshAt == 0 || c.ExpiresAt-c.RefreshAt != int64(refreshLead.Seconds()) {
		t.Errorf("refresh_at %d, expires_at %d; want renewal %v before expiry", c.RefreshAt, c.ExpiresAt, refreshLead)
	}
}

// An account whose credential holds no client secret, as one imported from today's auth folder
// does not (only the project), renews with the Antigravity app's built-in OAuth client.
func TestRefreshUsesTheAppsClient(t *testing.T) {
	srv := agtest.New(t)
	access, rt := srv.Grant()
	data, _ := json.Marshal(map[string]string{"project_id": agtest.Project})
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if c.AccessToken == access || c.RefreshToken != rt {
		t.Errorf("renewed credential %+v, want a new access token and the old refresh token", c)
	}
	forms := srv.TokenForms()
	if len(forms) != 1 || forms[0].Get("grant_type") != "refresh_token" ||
		forms[0].Get("client_id") != clientID || forms[0].Get("client_secret") != clientSecret {
		t.Errorf("the token endpoint saw %v, want one refresh_token request with the app's client ID and secret", forms)
	}
}

// A credential made by a login that asked for the secret still renews with the built-in one, and
// the renewed credential no longer holds the typed one.
func TestRefreshDropsTheTypedSecret(t *testing.T) {
	srv := agtest.New(t)
	access, rt := srv.Grant()
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt,
			Data: json.RawMessage(`{"project_id":"kept","client_secret":"typed-by-the-user"}`)}}})
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	if forms := srv.TokenForms(); len(forms) != 1 || forms[0].Get("client_secret") != clientSecret {
		t.Errorf("the token endpoint saw %v, want the built-in client secret", forms)
	}
	if got := readData(c); got.ProjectID != "kept" || strings.Contains(string(c.Data), "client_secret") {
		t.Errorf("renewed data %s, want the project kept and no client_secret", c.Data)
	}
}

// A login asks for no input to start: the app's client secret is built in.
func TestLoginAsksForNoInput(t *testing.T) {
	for _, m := range loginMethods {
		if len(m.Inputs) != 0 {
			t.Errorf("login method %q asks for %v", m.Method, m.Inputs)
		}
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
	cleaned, _ := cleanTools(json.RawMessage(in), false, false)
	_ = json.Unmarshal(cleaned, &got)
	_ = json.Unmarshal([]byte(want), &exp)
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(exp)
	if string(g) != string(w) {
		t.Errorf("cleaned tools\n %s\nwant\n %s", g, w)
	}
}

// Cloud Code's schema has no allOf (Google's published Schema lists anyOf and no allOf), so each
// allOf merges into the schema that holds it, as today's core merges it: the branches' required
// names join the schema's, and every other key a branch has fills the same key where the schema
// lacks it, recursively, the schema's own value winning. The merge comes after the description
// hints and before unions and type lists are flattened, and deepest first, so the result is the
// same as today's core's. Each expected value is today's core's output for the schema: its
// executor's tool cleaning (sanitizeAntigravityToolSchemaDocumentForModel) for
// other models, Gemini 3 Pro and Claude, and CleanJSONSchemaForAntigravityResponse for a response
// schema.
func TestAllOfMerged(t *testing.T) {
	for _, c := range []struct {
		name, schema                   string
		other, group, claude, response string
	}{
		{"two branches", `{"type":"object","allOf":[{"properties":{"a":{"type":"string"}},"required":["a"]},{"properties":{"b":{"type":"integer"}}}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"integer"}},"required":["a"],"type":"object"}`},
		{"parent plus branch", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"allOf":[{"properties":{"b":{"type":"string"}},"required":["b"]}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`},
		{"ref wrapper", `{"type":"object","properties":{"m":{"allOf":[{"$ref":"#/$defs/M"}],"description":"the m"}},"required":["m"],"$defs":{"M":{"type":"object","properties":{"x":{"type":"string","minLength":1}},"required":["x"]}}}`,
			`{"properties":{"m":{"description":"the m","properties":{"x":{"description":"minLength: 1","type":"string"}},"required":["x"],"type":"object"}},"required":["m"],"type":"object"}`,
			`{"properties":{"m":{"description":"the m","properties":{"x":{"description":"minLength: 1","type":"string"}},"required":["x"],"type":"object"}},"required":["m"],"type":"object"}`,
			`{"properties":{"m":{"description":"the m","properties":{"x":{"description":"minLength: 1","type":"string"}},"required":["x"],"type":"object"}},"required":["m"],"type":"object"}`,
			`{"properties":{"m":{"description":"the m","properties":{"x":{"description":"minLength: 1","type":"string"}},"required":["x"],"type":"object"}},"required":["m"],"type":"object"}`},
		{"nested", `{"allOf":[{"allOf":[{"properties":{"a":{"type":"string"}},"required":["a"]}]},{"type":"object"}]}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`},
		{"same property", `{"type":"object","properties":{"a":{"type":"string"}},"allOf":[{"properties":{"a":{"description":"from branch","enum":["x","y"]}},"required":["a"]}]}`,
			`{"properties":{"a":{"description":"from branch (Allowed: x, y)","type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"description":"from branch (Allowed: x, y)","type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"description":"from branch (Allowed: x, y)","type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"description":"from branch (Allowed: x, y)","enum":["x","y"],"type":"string"}},"required":["a"],"type":"object"}`},
		{"constraint parent desc", `{"type":"object","properties":{"s":{"description":"P","allOf":[{"type":"string","minLength":3}]}},"required":["s"]}`,
			`{"properties":{"s":{"description":"P","type":"string"}},"required":["s"],"type":"object"}`,
			`{"properties":{"s":{"description":"P","type":"string"}},"required":["s"],"type":"object"}`,
			`{"properties":{"s":{"description":"P","type":"string"}},"required":["s"],"type":"object"}`,
			`{"properties":{"s":{"description":"P","type":"string"}},"required":["s"],"type":"object"}`},
		{"items", `{"type":"object","properties":{"rows":{"type":"array","items":{"allOf":[{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}]}}},"required":["rows"]}`,
			`{"properties":{"rows":{"items":{"properties":{"id":{"type":"integer"}},"required":["id"],"type":"object"},"type":"array"}},"required":["rows"],"type":"object"}`,
			`{"properties":{"rows":{"items":{"properties":{"id":{"type":"integer"}},"required":["id"],"type":"object"},"type":"array"}},"required":["rows"],"type":"object"}`,
			`{"properties":{"rows":{"items":{"properties":{"id":{"type":"integer"}},"required":["id"],"type":"object"},"type":"array"}},"required":["rows"],"type":"object"}`,
			`{"properties":{"rows":{"items":{"properties":{"id":{"type":"integer"}},"required":["id"],"type":"object"},"type":"array"}},"required":["rows"],"type":"object"}`},
		{"union in branch", `{"type":"object","properties":{"v":{"allOf":[{"anyOf":[{"type":"string"},{"type":"integer"}]}],"description":"V"}},"required":["v"]}`,
			`{"properties":{"v":{"anyOf":[{"type":"string"},{"type":"integer"}],"description":"V"}},"required":["v"],"type":"object"}`,
			`{"properties":{"v":{"anyOf":[{"type":"string"},{"type":"integer"}],"description":"V"}},"required":["v"],"type":"object"}`,
			`{"properties":{"v":{"description":"V (Accepts: string | integer)","type":"string"}},"required":["v"],"type":"object"}`,
			`{"properties":{"v":{"anyOf":[{"type":"string"},{"type":"integer"}],"description":"V"}},"required":["v"],"type":"object"}`},
		{"union objects in branch", `{"type":"object","properties":{"v":{"allOf":[{"anyOf":[{"type":"object","properties":{"p":{"type":"string"}},"required":["p"]},{"type":"string"}]}]}},"required":["v"]}`,
			`{"properties":{"v":{"anyOf":[{"properties":{"p":{"type":"string"}},"required":["p"],"type":"object"},{"type":"string"}]}},"required":["v"],"type":"object"}`,
			`{"properties":{"v":{"anyOf":[{"properties":{"p":{"type":"string"}},"required":["p"],"type":"object"},{"type":"string"}]}},"required":["v"],"type":"object"}`,
			`{"properties":{"v":{"description":"Accepts: object | string","properties":{"p":{"type":"string"}},"required":["p"],"type":"object"}},"required":["v"],"type":"object"}`,
			`{"properties":{"v":{"anyOf":[{"properties":{"p":{"type":"string"}},"required":["p"],"type":"object"},{"type":"string"}]}},"required":["v"],"type":"object"}`},
		{"type list in branch", `{"type":"object","properties":{"n":{"description":"N","allOf":[{"type":["string","null"]}]}},"required":["n"]}`,
			`{"properties":{"n":{"description":"N ((nullable))","nullable":true,"type":"string"}},"required":["n"],"type":"object"}`,
			`{"properties":{"n":{"description":"N ((nullable))","nullable":true,"type":"string"}},"required":["n"],"type":"object"}`,
			`{"properties":{"n":{"description":"N ((nullable))","nullable":true,"type":"string"}},"required":["n"],"type":"object"}`,
			`{"properties":{"n":{"description":"N ((nullable))","nullable":true,"type":"string"}},"required":["n"],"type":"object"}`},
		{"if then", `{"type":"object","properties":{"k":{"type":"string"}},"required":["k"],"allOf":[{"if":{"properties":{"k":{"const":"a"}}},"then":{"required":["k"]}}]}`,
			`{"properties":{"k":{"type":"string"}},"required":["k"],"type":"object"}`,
			`{"properties":{"k":{"type":"string"}},"required":["k"],"type":"object"}`,
			`{"properties":{"k":{"type":"string"}},"required":["k"],"type":"object"}`,
			`{"properties":{"k":{"type":"string"}},"required":["k"],"type":"object"}`},
		{"nothing required", `{"type":"object","allOf":[{"properties":{"a":{"type":"string"}}},{"properties":{"b":{"type":"string"}}}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"type":"object"}`,
			`{"properties":{"_":{"type":"boolean"},"a":{"type":"string"},"b":{"type":"string"}},"required":["_"],"type":"object"}`,
			`{"properties":{"_":{"type":"boolean"},"a":{"type":"string"},"b":{"type":"string"}},"required":["_"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"type":"object"}`},
		{"untyped", `{"allOf":[{"type":"object","properties":{"a":{"type":"string"}}}]}`,
			`{"properties":{"a":{"type":"string"}},"type":"object"}`,
			`{"properties":{"_":{"type":"boolean"},"a":{"type":"string"}},"required":["_"],"type":"object"}`,
			`{"properties":{"_":{"type":"boolean"},"a":{"type":"string"}},"required":["_"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"type":"object"}`},
		{"branch underscore", `{"type":"object","allOf":[{"properties":{"_":{"type":"string"}}}]}`,
			`{"properties":{"_":{"type":"string"}},"type":"object"}`,
			`{"properties":{"_":{"type":"string"}},"required":["_"],"type":"object"}`,
			`{"properties":{"_":{"type":"string"}},"required":["_"],"type":"object"}`,
			`{"properties":{"_":{"type":"string"}},"type":"object"}`},
		{"required dup", `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"allOf":[{"required":["a","b"],"properties":{"b":{"type":"string"}}}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["a","b"],"type":"object"}`},
		{"title ap", `{"type":"object","allOf":[{"title":"T","additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"]}]}`,
			`{"description":"No extra properties allowed","properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"description":"No extra properties allowed","properties":{"a":{"type":"string"}},"required":["a"],"title":"T","type":"object"}`,
			`{"description":"No extra properties allowed","properties":{"a":{"type":"string"}},"required":["a"],"title":"T","type":"object"}`,
			""}, // the response schema is not compared: the extension drops a response schema's title, allOf or not, where today's core keeps it
		{"descs", `{"type":"object","description":"P","allOf":[{"description":"B","properties":{"a":{"type":"string"}},"required":["a"]}]}`,
			`{"description":"P","properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"description":"P","properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"description":"P","properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"description":"P","properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`},
		{"property named allOf", `{"type":"object","properties":{"allOf":{"type":"string"}},"required":["allOf"]}`,
			`{"properties":{"allOf":{"type":"string"}},"required":["allOf"],"type":"object"}`,
			`{"properties":{"allOf":{"type":"string"}},"required":["allOf"],"type":"object"}`,
			`{"properties":{"allOf":{"type":"string"}},"required":["allOf"],"type":"object"}`,
			`{"properties":{"allOf":{"type":"string"}},"required":["allOf"],"type":"object"}`},
		{"enum in branch", `{"type":"object","properties":{"c":{"allOf":[{"type":"string","enum":["r","g"]}]}},"required":["c"]}`,
			`{"properties":{"c":{"description":"Allowed: r, g","type":"string"}},"required":["c"],"type":"object"}`,
			`{"properties":{"c":{"description":"Allowed: r, g","type":"string"}},"required":["c"],"type":"object"}`,
			`{"properties":{"c":{"description":"Allowed: r, g","type":"string"}},"required":["c"],"type":"object"}`,
			`{"properties":{"c":{"description":"Allowed: r, g","enum":["r","g"],"type":"string"}},"required":["c"],"type":"object"}`},
		{"non object item", `{"type":"object","allOf":[true,{"properties":{"a":{"type":"string"}},"required":["a"]}]}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`},
		{"required only branch", `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"allOf":[{"required":["b"]}]}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["b"],"type":"object"}`,
			`{"properties":{"a":{"type":"string"},"b":{"type":"string"}},"required":["b"],"type":"object"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, m := range []struct {
				name          string
				claude, group bool
				want          string
			}{{"other models", false, false, c.other}, {"Gemini 3 Pro", false, true, c.group}, {"Claude", true, true, c.claude}} {
				out, _ := cleanTools(json.RawMessage(declaration(c.schema)), m.claude, m.group)
				var tools []struct {
					Decls []struct {
						Parameters json.RawMessage `json:"parameters"`
					} `json:"functionDeclarations"`
				}
				if err := json.Unmarshal(out, &tools); err != nil || len(tools) != 1 || len(tools[0].Decls) != 1 {
					t.Fatalf("%s: cleaned tools %s (%v)", m.name, out, err)
				}
				if got := string(tools[0].Decls[0].Parameters); !sameJSON(t, got, m.want) {
					t.Errorf("%s: parameters\n %s\nwant\n %s", m.name, got, m.want)
				}
			}
			if c.response == "" {
				return
			}
			gen := map[string]json.RawMessage{"responseSchema": json.RawMessage(c.schema)}
			cleanResponseSchema(gen)
			if got := string(gen["responseSchema"]); !sameJSON(t, got, c.response) {
				t.Errorf("response schema\n %s\nwant\n %s", got, c.response)
			}
		})
	}
}

// An allOf that is not a list is no JSON Schema, and Cloud Code takes no allOf: it is removed.
// Today's core keeps it as a property named allOf instead.
func TestAllOfNotAList(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"allOf":{"properties":{"b":{"type":"string"}}}}`
	want := `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`
	gen := map[string]json.RawMessage{"responseSchema": json.RawMessage(schema)}
	cleanResponseSchema(gen)
	if got := string(gen["responseSchema"]); !sameJSON(t, got, want) {
		t.Errorf("schema\n %s\nwant\n %s", got, want)
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
				"message": "bad request for token " + tok + " with secret " + clientSecret}})
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
		_, err := finishLogin(context.Background(), set, &tokenResponse{AccessToken: access, RefreshToken: rt, ExpiresIn: 3600})
		check("login "+path, err, access, rt, clientSecret)
	}
	srv := agtest.New(t)
	srv.Answer = echo("/v1internal:loadCodeAssist")
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{})
	_, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	if seen == "" || seen == access {
		t.Fatalf("renewal sent token %q to loadCodeAssist, want a new one", seen)
	}
	check("renewal", err, seen, clientSecret)
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
		err := relayStream(context.Background(), strings.NewReader(c.body), discard{}, nil)
		var e *llm.Error
		if !errors.As(err, &e) || e.Code != llm.CodeRateLimit || e.Retryable != c.retryable {
			t.Errorf("%s: err %v (retryable %v), want a rate limit with retryable %v", c.name, err, e != nil && e.Retryable, c.retryable)
		}
	}
}

type discard struct{}

func (discard) Send(llm.Event) error { return nil }

// The goroutine that forwards login input ends once the login's context is cancelled, even with
// input it cannot hand over: nothing reads the inputs after the login ends.
func TestForwardInputsStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	input := func(ctx context.Context) (map[string]string, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return map[string]string{"redirect_url": "x"}, nil
	}
	forwardInputs(ctx, input)
	// Let the forwarder fill the inputs and block handing over the next one.
	time.Sleep(50 * time.Millisecond)
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		if !strings.Contains(string(buf[:runtime.Stack(buf, true)]), "forwardInputs.func1") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the input forwarder still runs after the login was cancelled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
