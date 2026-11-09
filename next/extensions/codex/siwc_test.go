package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// siwcCred is a SIWC credential as the login makes it.
func siwcCred(access, refresh string) *protocol.Credential {
	data, _ := json.Marshal(credentialData{SIWC: true, ClientID: codextest.SIWCIssuedClientID,
		DeviceID: "8a4f2c1e-3b5d-4e6f-9a7b-1c2d3e4f5a6b", Scope: codextest.SIWCScope, IDToken: codextest.IDToken()})
	return &protocol.Credential{Kind: protocol.CredentialOAuth, AccessToken: access, RefreshToken: refresh, Data: data}
}

func formKeys(v url.Values) []string {
	var keys []string
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// The SIWC sign-in link asks for today's grant: the agent client, the API resource and the
// direct-use scope, PKCE S256, state, nonce, this device, and the identifier as the agent's name.
func TestSIWCAuthorizeURL(t *testing.T) {
	const device = "8a4f2c1e-3b5d-4e6f-9a7b-1c2d3e4f5a6b"
	for _, tc := range []struct{ setting, want string }{{"", "pluxy"}, {"  my-agent ", "my-agent"}} {
		u, err := url.Parse(siwcAuthURL(settings{SIWCIdentifier: tc.setting}, "verifier", "state-1", "nonce-1", device))
		if err != nil {
			t.Fatal(err)
		}
		if got := u.Scheme + "://" + u.Host + u.Path; got != "https://auth.openai.com/api/accounts/authorize" {
			t.Errorf("authorize URL %s", got)
		}
		q := u.Query()
		want := map[string]string{
			"client_id": "dynamic_agent_client", "response_type": "code", "agent_name_hint": tc.want,
			"ext_agent_host_id": "urn:uuid:" + device, "redirect_uri": "http://127.0.0.1:1455/auth/callback",
			"resource": "https://api.openai.com/v1", "scope": codextest.SIWCScope, "state": "state-1",
			"code_challenge": "iMnq5o6zALKXGivsnlom_0F5_WYda32GHkxlV7mq7hQ", "code_challenge_method": "S256", "nonce": "nonce-1",
		}
		if len(q) != len(want) {
			t.Errorf("parameters %v, want exactly %v", formKeys(q), want)
		}
		for k, v := range want {
			if q.Get(k) != v {
				t.Errorf("%s = %q, want %q", k, q.Get(k), v)
			}
		}
	}
	if id := randomUUID(); !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("device ID %q is not a lower-case version 4 UUID", id)
	}
}

// A SIWC sign-in exchanges its code as the issued client for the API resource, and the credential
// keeps what renewal needs; a renewal sends the issued client, the refresh token and the resource,
// keeps the ID token the reply leaves out, and is due five minutes before expiry.
func TestSIWCSignInAndRenewal(t *testing.T) {
	srv := codextest.New(t)
	acct := testAccount(srv, nil)
	set, _ := readSettings(acct.Settings)
	const device = "8a4f2c1e-3b5d-4e6f-9a7b-1c2d3e4f5a6b"
	verifier := randomString(64)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(siwcAuthURL(set, verifier, "st", "nonce", device))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || back.Query().Get("code") == "" {
		t.Fatalf("sign-in answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	g, err := siwcExchange(context.Background(), set, back.Query(), verifier, device)
	if err != nil {
		t.Fatal(err)
	}
	calls := srv.SIWCCalls()
	exchange := calls[len(calls)-1]
	if got := formKeys(exchange); !slices.Equal(got, []string{"client_id", "code", "code_verifier", "grant_type", "redirect_uri", "resource"}) {
		t.Errorf("exchange form has %v", got)
	}
	if exchange.Get("grant_type") != "authorization_code" || exchange.Get("client_id") != codextest.SIWCIssuedClientID ||
		exchange.Get("code_verifier") != verifier || exchange.Get("redirect_uri") != siwcRedirectURI || exchange.Get("resource") != siwcResource {
		t.Errorf("exchange form %v", exchange)
	}
	c := g.Credential
	var d credentialData
	_ = json.Unmarshal(c.Data, &d)
	if !d.SIWC || d.ClientID != codextest.SIWCIssuedClientID || d.DeviceID != device || d.Scope != codextest.SIWCScope || d.IDToken == "" || d.AccountID != "" {
		t.Errorf("data = %s", c.Data)
	}
	if c.ExpiresAt == 0 || c.RefreshAt != c.ExpiresAt-300 {
		t.Errorf("expires_at %d, refresh_at %d: want renewal five minutes ahead", c.ExpiresAt, c.RefreshAt)
	}
	if g.Label != codextest.Email || g.Identity["email"] != codextest.Email || g.Identity["account_id"] != codextest.AccountID {
		t.Errorf("label %q, identity %v", g.Label, g.Identity)
	}

	renewed, err := refresh(context.Background(), sdk.RefreshCall{Account: testAccount(srv, c)})
	if err != nil {
		t.Fatal(err)
	}
	calls = srv.SIWCCalls()
	form := calls[len(calls)-1]
	if got := formKeys(form); !slices.Equal(got, []string{"client_id", "grant_type", "refresh_token", "resource"}) {
		t.Errorf("refresh form has %v", got)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != codextest.SIWCIssuedClientID ||
		form.Get("refresh_token") != c.RefreshToken || form.Get("resource") != siwcResource {
		t.Errorf("refresh form %v", form)
	}
	var rd credentialData
	_ = json.Unmarshal(renewed.Data, &rd)
	if renewed.AccessToken == c.AccessToken || renewed.RefreshToken == c.RefreshToken || rd != d {
		t.Errorf("renewed %+v, data %s; want new tokens and the same data", renewed, renewed.Data)
	}
	// The old refresh token was used up, and the Codex token endpoint was never called.
	if _, err := refresh(context.Background(), sdk.RefreshCall{Account: testAccount(srv, c)}); err == nil {
		t.Error("a used refresh token was accepted again")
	}
	if srv.RefreshCalls() != 2 {
		t.Errorf("refresh calls %d, want 2", srv.RefreshCalls())
	}
}

// A grant without the direct-use scope cannot call the API, a sign-in result without the issued
// client cannot be exchanged, a sign-in reply without an ID token or a renewal without a refresh
// token is incomplete, and a credential without its issued client cannot be renewed.
func TestSIWCGrantChecks(t *testing.T) {
	srv := codextest.New(t)
	srv.SIWCGrantScope = "openid profile email offline_access"
	set, _ := readSettings(testAccount(srv, nil).Settings)
	_, refreshToken := srv.SIWCGrant()
	_, err := refresh(context.Background(), sdk.RefreshCall{Account: testAccount(srv, siwcCred("a", refreshToken))})
	wantError(t, err, llm.CodeAuth, false)

	_, err = siwcExchange(context.Background(), set, url.Values{"code": {"c"}, "state": {"s"}}, "v", "d")
	wantError(t, err, llm.CodeAuth, false)
	if n := len(srv.SIWCCalls()); n != 1 {
		t.Errorf("a result without an issued client reached the token endpoint (%d calls)", n)
	}

	noID := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"a","refresh_token":"r","scope":"`+codextest.SIWCScope+`","expires_in":3600}`)
	}))
	defer noID.Close()
	raw, _ := json.Marshal(map[string]any{"auth_base_url": noID.URL})
	set, _ = readSettings(raw)
	_, err = siwcExchange(context.Background(), set, url.Values{"code": {"c"}, "client_id": {"x"}}, "v", "d")
	wantError(t, err, llm.CodeUpstream, false)

	// A renewal reply without a refresh token is refused, as today's core refuses it, so core
	// keeps the stored credential and its refresh token.
	noRefresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"a2","scope":"`+codextest.SIWCScope+`","expires_in":3600}`)
	}))
	defer noRefresh.Close()
	raw, _ = json.Marshal(map[string]any{"auth_base_url": noRefresh.URL})
	renewed, err := refresh(context.Background(), sdk.RefreshCall{Account: protocol.Account{Settings: raw, Credential: siwcCred("a", "r")}})
	wantError(t, err, llm.CodeUpstream, false)
	if renewed != nil {
		t.Errorf("renewed without a refresh token: %+v", renewed)
	}

	c := siwcCred("a", "r")
	c.Data = json.RawMessage(`{"siwc":true}`)
	_, err = refresh(context.Background(), sdk.RefreshCall{Account: testAccount(srv, c)})
	wantError(t, err, llm.CodeAuth, false)
}

// siwcRequest builds the upstream request generate would send on a SIWC account.
func siwcRequest(t *testing.T, req llm.Request, s settings) (*upstreamRequest, http.Header, map[string]any) {
	t.Helper()
	a, err := accountOf(siwcCred("siwc-access", "r"))
	if err != nil {
		t.Fatal(err)
	}
	ur, err := buildRequest(req, "acct", s, a, false)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	setHeaders(h, req, ur, s, a)
	var body map[string]any
	_ = json.Unmarshal(ur.body, &body)
	return ur, h, body
}

// A SIWC account's request goes out under today's SIWC rules: the SIWC token and User-Agent, the
// session as session_id and X-Client-Request-Id and none of the Codex headers; no image tool;
// configuration_update items folded into the reasoning effort; blank instructions left out.
func TestSIWCRequestRules(t *testing.T) {
	req := wsRequest(t, `{"model":"gpt-6.1-sol","instructions":"  ","temperature":0.2,"prompt_cache_retention":"24h",
		"store":true,"prompt_cache_key":"cache-1","reasoning":{"effort":"medium"},
		"input":[{"type":"configuration_update","reasoning":{"effort":"Low"}},
		{"role":"user","content":"hi"},{"type":"configuration_update","reasoning":{"effort":"HIGH"}},
		{"type":"configuration_update","reasoning":{"effort":""}}]}`)
	req.Native.Headers = map[string]string{"originator": "my-client", "x-codex-window-id": "w1", "session-id": "s1", "user-agent": "my-client/1.0"}
	_, h, body := siwcRequest(t, req, settings{})
	for name, want := range map[string]string{
		"Authorization": "Bearer siwc-access", "User-Agent": "pluxy (" + runtime.GOOS + "; " + runtime.GOARCH + ")",
		"X-Client-Request-Id": "cache-1", "Accept": "text/event-stream",
		"Originator": "", "Chatgpt-Account-Id": "", "X-Codex-Routing-Hint": "", "X-Codex-Window-Id": "", "Session-Id": "",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if got := h["session_id"]; len(got) != 1 || got[0] != "cache-1" {
		t.Errorf("session_id %v, want cache-1", got)
	}
	if _, ok := body["instructions"]; ok {
		t.Errorf("blank instructions sent: %v", body["instructions"])
	}
	for _, k := range []string{"temperature", "prompt_cache_retention"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s sent", k)
		}
	}
	if body["store"] != false || body["stream"] != true {
		t.Errorf("store %v, stream %v", body["store"], body["stream"])
	}
	if eff := body["reasoning"].(map[string]any)["effort"]; eff != "high" {
		t.Errorf("effort %v, want the last configuration_update's", eff)
	}
	if b, _ := json.Marshal(body); strings.Contains(string(b), "configuration_update") || strings.Contains(string(b), "image_generation") {
		t.Errorf("body %s keeps a configuration_update item or offers the image tool", b)
	}

	// A thinking suffix's effort wins over the items, which are still removed; nonblank
	// instructions stay; the identifier setting names the User-Agent.
	req = wsRequest(t, `{"model":"gpt-6.1-sol","instructions":"Be brief.","input":[{"role":"user","content":"hi"},
		{"type":"configuration_update","reasoning":{"effort":"high"}}]}`)
	req.Options.ThinkingSuffix = "low"
	_, h, body = siwcRequest(t, req, settings{SIWCIdentifier: "agent-x"})
	if eff := body["reasoning"].(map[string]any)["effort"]; eff != "low" || body["instructions"] != "Be brief." {
		t.Errorf("effort %v, instructions %v", eff, body["instructions"])
	}
	if items := body["input"].([]any); len(items) != 1 {
		t.Errorf("input %v, want the configuration_update removed", items)
	}
	if got := h.Get("User-Agent"); got != "agent-x ("+runtime.GOOS+"; "+runtime.GOARCH+")" {
		t.Errorf("User-Agent %q", got)
	}

	if got := baseURL(settings{}, account{siwc: true}); got != "https://api.openai.com/v1" {
		t.Errorf("default base URL %q", got)
	}
	if _, err := readSettings(json.RawMessage(`{"signin_with_chatgpt_identifier":"a\r\nX-Evil: 1"}`)); err == nil {
		t.Error("an identifier with a line break was accepted")
	}
}

// capture is an upstream that records each request and refuses it, so a call ends before it
// needs a reply stream.
type capture struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	body []map[string]any
}

func newCapture(t *testing.T) *capture {
	c := &capture{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		c.mu.Lock()
		c.reqs, c.body = append(c.reqs, r), append(c.body, b)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"captured"}}`)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func siwcCall(c *capture, format, body string, headers map[string]string) sdk.Call {
	settings, _ := json.Marshal(map[string]any{"base_url": c.URL + "/v1"})
	return sdk.Call{
		Account: protocol.Account{ID: "a", Settings: settings, Credential: siwcCred("siwc-access", "r")},
		Request: llm.Request{Model: "gpt-6.1-sol", Native: &llm.Native{Format: format, Body: json.RawMessage(body), Headers: headers}},
	}
}

// Compaction on a SIWC account goes to {base_url}/responses/compact with the SIWC token and
// headers, without stream, store or the fields the API refuses.
func TestSIWCCompaction(t *testing.T) {
	c := newCapture(t)
	call := siwcCall(c, llm.FormatOpenAIResponsesCompact,
		`{"model":"gpt-6.1-sol","input":[{"role":"user","content":"hi"},{"type":"configuration_update","reasoning":{"effort":"high"}}],"store":true,"instructions":"","temperature":1}`,
		map[string]string{"session-id": "s1"})
	err := endpoint(context.Background(), call, nil)
	wantError(t, err, llm.CodeInvalidRequest, false)
	if c.count() != 1 {
		t.Fatalf("%d upstream requests", c.count())
	}
	r, b := c.reqs[0], c.body[0]
	if r.URL.Path != "/v1/responses/compact" || r.Header.Get("Authorization") != "Bearer siwc-access" ||
		!strings.HasPrefix(r.Header.Get("User-Agent"), "pluxy (") || r.Header.Get("Accept") != "application/json" || r.Header.Get("Originator") != "" {
		t.Errorf("request %s %v", r.URL.Path, r.Header)
	}
	for _, k := range []string{"store", "stream", "instructions", "temperature"} {
		if _, ok := b[k]; ok {
			t.Errorf("%s sent: %v", k, b)
		}
	}
	if b["reasoning"].(map[string]any)["effort"] != "high" || len(b["input"].([]any)) != 1 {
		t.Errorf("body %v", b)
	}
}

// A SIWC account refuses, for this account only so core tries another, a Codex client's request
// (on HTTP, compaction and the WebSocket alike) and every endpoint but compaction; it has no
// quota to report. None of these reaches the upstream.
func TestSIWCRefusals(t *testing.T) {
	c := newCapture(t)
	codex := map[string]string{"user-agent": "codex_cli_rs/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11"}
	unsupported := func(what string, err error) {
		t.Helper()
		var e *llm.Error
		if !asError(err, &e) || e.Code != llm.CodeUnsupported || e.Scope != llm.ScopeCredential || !e.Retryable {
			t.Errorf("%s: error = %v, want unsupported for the account, retryable", what, err)
		}
	}
	unsupported("Codex client", generate(context.Background(), siwcCall(c, llm.FormatOpenAIResponses, `{"model":"gpt-6.1-sol","input":"hi"}`, codex), nil))
	unsupported("Codex client compaction", endpoint(context.Background(), siwcCall(c, llm.FormatOpenAIResponsesCompact, `{"model":"gpt-6.1-sol","input":[]}`, codex), nil))
	for _, f := range []string{llm.FormatOpenAIImagesGenerations, llm.FormatOpenAIImagesEdits, "openai.some.later.endpoint"} {
		unsupported(f, endpoint(context.Background(), siwcCall(c, f, `{"model":"gpt-image-2","prompt":"p"}`, nil), nil))
	}
	sess, acct := wsSessionFor(t, newFakeWS(t), siwcCred("siwc-access", "r"))
	turn := siwcCall(c, llm.FormatOpenAIResponses, `{"model":"gpt-6.1-sol","input":[]}`, codex)
	turn.Account = acct
	unsupported("Codex client turn", sess.Turn(context.Background(), turn, nil))
	// A quota call is not routed, so its refusal is not retryable (as for an API key).
	_, err := quota(context.Background(), sdk.QuotaCall{Account: siwcCall(c, "", "{}", nil).Account})
	wantError(t, err, llm.CodeUnsupported, false)
	if c.count() != 0 {
		t.Errorf("%d requests reached the upstream", c.count())
	}

	// Other clients' requests are served.
	err = generate(context.Background(), siwcCall(c, llm.FormatOpenAIResponses, `{"model":"gpt-6.1-sol","input":"hi"}`,
		map[string]string{"user-agent": "OpenAI/Python 2.1.0"}), nil)
	wantError(t, err, llm.CodeInvalidRequest, false)
	if c.count() != 1 || c.reqs[0].URL.Path != "/v1/responses" || c.reqs[0].Header.Get("Authorization") != "Bearer siwc-access" {
		t.Errorf("a non-Codex client's request was not sent as SIWC")
	}
}

// A SIWC socket is opened with the SIWC token and headers, and its turns go without stream and
// background, not stored.
func TestSIWCSocket(t *testing.T) {
	f := newFakeWS(t)
	cred := siwcCred("siwc-access", "r")
	s, acct := wsSessionFor(t, f, cred)
	a, err := accountOf(cred)
	if err != nil {
		t.Fatal(err)
	}
	req := wsRequest(t, `{"model":"gpt-6.1-sol","prompt_cache_key":"cache-1","background":true,"stream":true,"input":[]}`)
	req.Native.Headers = map[string]string{"originator": "my-client", "x-codex-window-id": "w1"}
	ur, err := buildRequest(req, acct.ID, s.set, a, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.dial(context.Background(), req, ur, a); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	h := f.headers[0]
	f.mu.Unlock()
	for name, want := range map[string]string{
		"Authorization": "Bearer siwc-access", "Openai-Beta": openAIBeta, "Session_id": "cache-1", "X-Client-Request-Id": "cache-1",
		"User-Agent":      "pluxy (" + runtime.GOOS + "; " + runtime.GOARCH + ")",
		"Conversation_id": "", "Originator": "", "Chatgpt-Account-Id": "", "X-Codex-Routing-Hint": "", "X-Codex-Window-Id": "",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	var body map[string]any
	_ = json.Unmarshal(ur.body, &body)
	_, stream := body["stream"]
	_, background := body["background"]
	if stream || background || body["store"] != false {
		t.Errorf("frame body %s", ur.body)
	}
}

// A SIWC account lists its plan's models without the image models.
func TestSIWCModels(t *testing.T) {
	c := siwcCred("a", "r")
	c.Data, _ = json.Marshal(credentialData{SIWC: true, IDToken: idTokenWithPlan("plus")})
	got, err := listModels(context.Background(), sdk.ModelsCall{Account: protocol.Account{Credential: c}})
	if err != nil {
		t.Fatal(err)
	}
	plus := planModels["plus"]
	if len(got.Models) != len(plus)-len(imageModels) || len(got.Models) == 0 {
		t.Fatalf("%d models, want the %d plus models without the %d image models", len(got.Models), len(plus), len(imageModels))
	}
	for i, m := range got.Models {
		if imageModel(m.ID) != "" || m.ID != plus[i].ID {
			t.Errorf("model %d is %s", i, m.ID)
		}
	}
}
