package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/extensions/xai/xaitest"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

func sameJSON(t *testing.T, what string, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("%s: %v: %s", what, err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("%s: bad want: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

// responsesBody builds the body for a Responses client request, the way the Responses format
// reads it, and returns the body the extension sends.
func responsesBody(t *testing.T, body string) []byte {
	t.Helper()
	req, err := openairesponses.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStreamFlagFollowsClient(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[{"role":"user","content":"hi"}]}`)
	sameJSON(t, "non-stream", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[{"role":"user","content":"hi"}]}`)
	got = responsesBody(t, `{"model":"grok-4.6","stream":true,"input":[{"role":"user","content":"hi"}]}`)
	sameJSON(t, "stream", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":true,"input":[{"role":"user","content":"hi"}]}`)
}

func TestRemovedFields(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"stop":["x"],"previous_response_id":"resp_1",
		"prompt_cache_retention":"24h","safety_identifier":"u","stream_options":{"include_usage":true},"temperature":0.2}`)
	sameJSON(t, "removed fields", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[],"temperature":0.2}`)
}

// Responses are not kept on xAI unless the client asks, and encrypted reasoning is asked for
// alongside the client's own include values.
func TestStoreAndInclude(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"store":true,"include":["web_search_call.action.sources"]}`)
	sameJSON(t, "client store and include", got, `{"model":"grok-4.6","stream":false,"input":[],"store":true,
		"include":["web_search_call.action.sources","reasoning.encrypted_content"]}`)
	got = responsesBody(t, `{"model":"grok-4.6","input":[],"include":["reasoning.encrypted_content"]}`)
	sameJSON(t, "include once", got, `{"model":"grok-4.6","stream":false,"input":[],"store":false,"include":["reasoning.encrypted_content"]}`)
}

func TestCustomToolBecomesFunction(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[
		{"type":"custom","name":"run_shell","description":"run","format":{"type":"grammar","syntax":"lark","definition":"x"}}]}`)
	sameJSON(t, "custom tool", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[],"tools":[
		{"type":"function","name":"run_shell","description":"run","format":{"type":"grammar","syntax":"lark","definition":"x"},"parameters":{"type":"object","properties":{}}}]}`)
}

func TestFunctionSchemaInlined(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f","parameters":{
		"type":"object","$defs":{"C":{"type":"string","enum":["a","b"]}},"properties":{"c":{"$ref":"#/$defs/C"}}}}]}`)
	sameJSON(t, "schema", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[],"tools":[{"type":"function","name":"f","parameters":{
		"type":"object","properties":{"c":{"type":"string","enum":["a","b"]}}}}]}`)
	// A definitions block is removed too, and an $ref in an array is inlined.
	got = responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f","parameters":{
		"type":"object","definitions":{"N":{"type":"integer"}},"properties":{"xs":{"type":"array","items":{"$ref":"#/definitions/N"}}}}}]}`)
	if strings.Contains(string(got), "definitions") || strings.Contains(string(got), "$ref") {
		t.Errorf("definitions or $ref left: %s", got)
	}
	if !strings.Contains(string(got), `"items":{"type":"integer"}`) {
		t.Errorf("array $ref not inlined: %s", got)
	}
}

func TestToolLimit(t *testing.T) {
	var tools []string
	for i := 0; i < maxTools+5; i++ {
		tools = append(tools, `{"type":"function","name":"t`+strings.Repeat("x", i%3)+`"}`)
	}
	got := responsesBody(t, `{"model":"grok-4.6","input":[],"tools":[`+strings.Join(tools, ",")+`]}`)
	var m struct {
		Tools []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(got, &m)
	if len(m.Tools) != maxTools {
		t.Fatalf("tools = %d, want %d", len(m.Tools), maxTools)
	}
	// A function without parameters gets the empty object schema, as in the core.
	var first struct {
		Parameters map[string]any `json:"parameters"`
	}
	_ = json.Unmarshal(m.Tools[0], &first)
	if !reflect.DeepEqual(first.Parameters, map[string]any{"type": "object", "properties": map[string]any{}}) {
		t.Errorf("first tool parameters = %v", first.Parameters)
	}
}

func TestCustomToolItems(t *testing.T) {
	got := responsesBody(t, `{"model":"grok-4.6","input":[
		{"type":"custom_tool_call","call_id":"c1","name":"run_shell","input":"ls -la"},
		{"type":"custom_tool_call_output","call_id":"c1","output":"done"},
		{"type":"custom_tool_call","call_id":"c2","name":"f","input":{"path":"a"}},
		{"type":"custom_tool_call","call_id":"","name":"f","input":"x"}]}`)
	sameJSON(t, "custom items", got, `{"model":"grok-4.6","store":false,"include":["reasoning.encrypted_content"],"stream":false,"input":[
		{"type":"function_call","call_id":"c1","name":"run_shell","arguments":"{\"input\":\"ls -la\"}"},
		{"type":"function_call_output","call_id":"c1","output":"done"},
		{"type":"function_call","call_id":"c2","name":"f","arguments":"{\"path\":\"a\"}"}]}`)
}

func TestChatClientTranslatedToResponses(t *testing.T) {
	req, err := openaichat.ParseRequest([]byte(`{"model":"grok-4.6","stream":true,"reasoning_effort":"high",
		"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	out, tr, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	if tr.To != llm.FormatOpenAIResponses || tr.Passthrough {
		t.Errorf("translation = %+v", tr)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["stream"] != true || m["model"] != "grok-4.6" {
		t.Errorf("body = %s", out)
	}
	if r, _ := m["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Errorf("reasoning = %v, want effort high", m["reasoning"])
	}
}

func errorFor(status int, header http.Header, body string) *llm.Error {
	resp := &http.Response{StatusCode: status, Status: http.StatusText(status), Header: header, Body: io.NopCloser(strings.NewReader(body))}
	return upstreamError(resp, "grok-4.6")
}

func TestErrorMapping(t *testing.T) {
	none := http.Header{}
	cases := []struct {
		name   string
		status int
		header http.Header
		body   string
		code   string
		scope  string
		retry  bool
		ms     int64
	}{
		{"invalid key", 401, none, `{"code":"Client specified an invalid argument","error":"Incorrect API key provided."}`, llm.CodeAuth, llm.ScopeCredential, true, 0},
		{"bad credentials on 403", 403, none, `{"error":{"message":"access token could not be validated","code":"bad-credentials"}}`, llm.CodeAuth, llm.ScopeCredential, true, 0},
		{"other 403", 403, none, `{"error":{"message":"The team has no access to this model","type":"permission_error"}}`, llm.CodePermission, llm.ScopeCredential, true, 0},
		{"free usage used up", 429, none, `{"code":"subscription:free-usage-exhausted","error":"included free usage used"}`, llm.CodeQuota, llm.ScopeCredential, true, 24 * 60 * 60 * 1000},
		{"rate limit with Retry-After", 429, http.Header{"Retry-After": {"20"}}, `{"code":"Too many requests","error":"Rate limit reached"}`, llm.CodeRateLimit, llm.ScopeCredential, true, 20000},
		{"model not found", 404, none, `{"error":{"message":"The model grok-4.6 does not exist"}}`, llm.CodeNotFound, llm.ScopeModel, true, 0},
		{"unavailable", 503, http.Header{"Retry-After": {"5"}}, `{"error":"overloaded"}`, llm.CodeUnavailable, llm.ScopeEndpoint, false, 5000},
		{"server error", 500, none, `oops`, llm.CodeUpstream, llm.ScopeEndpoint, false, 0},
		{"bad request", 400, none, `{"error":{"message":"bad input"}}`, llm.CodeInvalidRequest, llm.ScopeRequest, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := errorFor(c.status, c.header, c.body)
			if e.Code != c.code || e.Scope != c.scope || e.Retryable != c.retry || e.RetryAfterMS != c.ms {
				t.Errorf("got code=%s scope=%s retryable=%v retry_after=%d, want code=%s scope=%s retryable=%v retry_after=%d",
					e.Code, e.Scope, e.Retryable, e.RetryAfterMS, c.code, c.scope, c.retry, c.ms)
			}
		})
	}
}

func TestRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if got := retryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); got != 90000 {
		t.Errorf("retryAfter(date) = %d, want 90000", got)
	}
	if got := retryAfter("soon", now); got != 0 {
		t.Errorf("retryAfter(bad) = %d, want 0", got)
	}
}

func TestSettingsDefaultBaseURL(t *testing.T) {
	s, err := readSettings(nil)
	if err != nil || s.BaseURL != "https://api.x.ai/v1" {
		t.Errorf("default = %+v, %v", s, err)
	}
	s, err = readSettings(json.RawMessage(`{"base_url":"http://127.0.0.1:9/v1/"}`))
	if err != nil || s.BaseURL != "http://127.0.0.1:9/v1" {
		t.Errorf("trimmed = %+v, %v", s, err)
	}
}

type promptFunc func(protocol.Prompt)

func (f promptFunc) Prompt(p protocol.Prompt) error { f(p); return nil }

// A Grok login: the device code is approved, and the credential holds the tokens, the expiry with
// renewal five minutes before it, the token endpoint discovery named, and the ID token's e-mail
// address as its label.
func TestDeviceLogin(t *testing.T) {
	srv := xaitest.New(t)
	set, _ := readSettings(srv.Settings())
	g, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) }))
	if err != nil {
		t.Fatal(err)
	}
	c := g.Credential
	var data credentialData
	_ = json.Unmarshal(c.Data, &data)
	if c.Kind != protocol.CredentialOAuth || c.AccessToken == "" || c.RefreshToken == "" || g.Label != xaitest.Email || data.TokenEndpoint != srv.URL+"/oauth2/token" {
		t.Errorf("grant %+v label %q data %+v", c, g.Label, data)
	}
	if c.ExpiresAt-c.RefreshAt != int64(refreshLead.Seconds()) {
		t.Errorf("refresh_at %d, expires_at %d; want renewal %v before expiry", c.RefreshAt, c.ExpiresAt, refreshLead)
	}
}

// A device poll that gets an outage with no OAuth error is tried again; one that gets another
// error ends the login without echoing the device code.
func TestDevicePollErrors(t *testing.T) {
	srv := xaitest.New(t)
	failed := false
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oauth2/token" || failed {
			return false
		}
		failed = true
		http.Error(w, "upstream connect error", http.StatusServiceUnavailable)
		return true
	}
	set, _ := readSettings(srv.Settings())
	if _, err := deviceLogin(context.Background(), set, promptFunc(func(p protocol.Prompt) { srv.Approve(p.UserCode) })); err != nil || !failed {
		t.Fatalf("login after an outage: err %v (outage seen %v)", err, failed)
	}
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oauth2/token" {
			return false
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"server_error","error_description":"bad code `+r.PostForm.Get("device_code")+`"}`)
		return true
	}
	_, err := deviceLogin(context.Background(), set, promptFunc(func(protocol.Prompt) {}))
	if err == nil || strings.Contains(err.Error(), "xaitest-device-") || !strings.Contains(err.Error(), "server_error") {
		t.Errorf("poll error %v: want server_error without the device code", err)
	}
}

// Login endpoints must be HTTPS on x.ai, or on the configured issuer's own address; a discovery
// document or credential naming another host is not trusted with the user's codes and tokens.
func TestLoginEndpointsChecked(t *testing.T) {
	for _, c := range []struct {
		url, issuer string
		ok          bool
	}{
		{"https://auth.x.ai/oauth2/token", defaultIssuer, true},
		{"https://accounts.x.ai/token", defaultIssuer, true},
		{"http://auth.x.ai/token", defaultIssuer, false},
		{"https://x.ai.example.com/token", defaultIssuer, false},
		{"https://evil.example/token", defaultIssuer, false},
		{"http://127.0.0.1:9/token", "http://127.0.0.1:9", true},
		{"http://127.0.0.1:8/token", "http://127.0.0.1:9", false},
	} {
		if got := allowedEndpoint(c.url, c.issuer); got != c.ok {
			t.Errorf("allowedEndpoint(%q, %q) = %v, want %v", c.url, c.issuer, got, c.ok)
		}
	}
	srv := xaitest.New(t)
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/.well-known/openid-configuration" {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"device_authorization_endpoint":"https://evil.example/device","token_endpoint":"`+srv.URL+`/oauth2/token"}`)
		return true
	}
	set, _ := readSettings(srv.Settings())
	if _, err := discover(context.Background(), set); err == nil {
		t.Error("discovery naming a foreign device endpoint was accepted")
	}
}

// A renewal sends the refresh token to the token endpoint the credential holds, or, when that is
// not one xAI's could be, to the one discovery names.
func TestRefreshEndpoint(t *testing.T) {
	srv := xaitest.New(t)
	access, rt := srv.Grant()
	data, _ := json.Marshal(credentialData{TokenEndpoint: "https://evil.example/token"})
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: rt, Data: data}}})
	if err != nil {
		t.Fatal(err)
	}
	var got credentialData
	_ = json.Unmarshal(c.Data, &got)
	if c.AccessToken == access || c.RefreshToken == rt || got.TokenEndpoint != srv.URL+"/oauth2/token" {
		t.Errorf("renewed %+v with endpoint %q; want new tokens from the discovered endpoint", c, got.TokenEndpoint)
	}
}

// A Grok login's requests go to the Grok CLI's endpoint, or to base_url when it names another
// address, and carry the CLI's identity; an API key's go to base_url with pluxy's.
func TestLoginRequests(t *testing.T) {
	def, _ := readSettings(nil)
	login := &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "a"}
	if tg, _ := accountTarget(def, login); tg.base != cliChatBaseURL || !tg.cli {
		t.Errorf("login with no base_url goes to %q (cli %v), want %q", tg.base, tg.cli, cliChatBaseURL)
	}
	if tg, _ := accountTarget(def, &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "k"}); tg.base != defaultBaseURL || tg.cli {
		t.Errorf("API key goes to %q (cli %v), want %q", tg.base, tg.cli, defaultBaseURL)
	}
	srv := xaitest.New(t)
	// The fake refuses the request, so generate ends before it would stream to the core.
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req xaitest.Request) {
		http.Error(w, `{"error":"stop here"}`, http.StatusBadRequest)
	}
	access, _ := srv.Grant()
	err := generate(context.Background(), sdk.Call{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access}},
		Request: llm.Request{Model: "grok-4.7", Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "Hi"}}}}}}, nil)
	if err == nil {
		t.Fatal("generate succeeded against a refusing fake")
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests reached the fake, want 1", len(reqs))
	}
	h := reqs[0].Header
	if h.Get("Authorization") != "Bearer "+access || h.Get("User-Agent") != "xai-grok-workspace/"+cliVersion || h.Get("X-XAI-Token-Auth") != "xai-grok-cli" ||
		h.Get("x-grok-client-version") != cliVersion || h.Get("x-grok-client-identifier") != "grok-shell" || h.Get("x-authenticateresponse") != "authenticate-response" {
		t.Errorf("login request headers %v", h)
	}
}

// A renewal whose reply has no refresh token keeps the one the credential holds, as today's core
// keeps it.
func TestRefreshKeepsRefreshToken(t *testing.T) {
	srv := xaitest.New(t)
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oauth2/token" {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"renewed-access","expires_in":3600}`)
		return true
	}
	c, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: srv.Settings(),
		Credential: &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: "old-access", RefreshToken: "kept-refresh"}}})
	if err != nil || c.AccessToken != "renewed-access" || c.RefreshToken != "kept-refresh" {
		t.Errorf("renewed %+v, err %v; want the new access token and the kept refresh token", c, err)
	}
}

// slow_down makes every later poll wait 5 seconds longer, as RFC 8628 and today's core have it.
func TestDeviceSlowDown(t *testing.T) {
	srv := xaitest.New(t)
	slowed := false
	srv.Answer = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/oauth2/token" || slowed {
			return false
		}
		slowed = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"slow_down"}`)
		return true
	}
	set, _ := readSettings(srv.Settings())
	var code string
	approveLater := promptFunc(func(p protocol.Prompt) { code = p.UserCode })
	// The waits are recorded instead of slept; the user approves during the second.
	var waits []time.Duration
	old := waitPoll
	t.Cleanup(func() { waitPoll = old })
	waitPoll = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		if len(waits) == 2 {
			srv.Approve(code)
		}
		return ctx.Err() == nil
	}
	if _, err := deviceLogin(context.Background(), set, approveLater); err != nil {
		t.Fatal(err)
	}
	if want := []time.Duration{10 * time.Second, 10 * time.Second}; len(waits) != 2 || waits[0] != want[0] || waits[1] != want[1] {
		t.Errorf("waits %v, want %v", waits, want)
	}
}

// A refused login call keeps xAI's Retry-After for a rate limit or an outage; a refused grant
// asks for a new login and is not retried.
func TestLoginFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		ra     string
		body   string
		code   string
		retry  bool
		ms     int64
	}{
		{"rate limit", 429, "30", `{"error":"slow_down"}`, llm.CodeRateLimit, true, 30000},
		{"outage", 503, "5", ``, llm.CodeUnavailable, true, 5000},
		{"refused grant", 400, "", `{"error":"invalid_grant"}`, llm.CodeAuth, false, 0},
	} {
		e := loginFailure(c.status, []byte(c.body), http.Header{"Retry-After": {c.ra}})
		if e.Code != c.code || e.Retryable != c.retry || e.RetryAfterMS != c.ms {
			t.Errorf("%s: %+v, want %s retryable %v after %d ms", c.name, e, c.code, c.retry, c.ms)
		}
	}
}
