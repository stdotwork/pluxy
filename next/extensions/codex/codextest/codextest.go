// Package codextest is a fake of OpenAI's login server and the Codex Responses endpoint, for
// testing the Codex provider extension without reaching OpenAI. It speaks the login endpoints the
// extension uses (the authorization code grant with PKCE, OpenAI's device-code flow and refresh),
// answers POST /responses and /backend-api/codex/responses with scripted streams, and answers
// GET /backend-api/wham/usage, the ChatGPT usage endpoint. It also speaks Sign in with ChatGPT
// (SIWC): GET /api/accounts/authorize, whose result carries the client ID it issues, and
// POST /api/accounts/oauth/token for that client.
//
// Tokens are unsigned JWTs. Refresh tokens rotate: each one works once, and using one again is
// refused as refresh_token_reused, as OpenAI does.
package codextest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/protocol"
)

// ClientID is the OAuth client the extension must identify as.
const ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// SIWC: the client a SIWC sign-in asks to register, the client ID the fake issues to it, the API
// the tokens are for, and the scope it grants.
const (
	SIWCClientID       = "dynamic_agent_client"
	SIWCIssuedClientID = "siwc-client-codextest"
	SIWCResource       = "https://api.openai.com/v1"
	SIWCScope          = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

// Account is the ChatGPT account every sign-in belongs to.
const (
	AccountID = "acct-codextest"
	Email     = "codex@example.invalid"
	PlanType  = "plus"
)

// Server is the fake. Set its exported fields before use.
type Server struct {
	*httptest.Server

	// DenyBrowser makes the browser sign-in end with error=access_denied.
	DenyBrowser bool
	// RefreshFailure "unavailable" makes refresh answer 503.
	RefreshFailure string
	// Usage, if set, is the body of the usage endpoint's reply; the default is DefaultUsage.
	Usage string
	// AccessTTL is how long the n-th issued access token lasts (n from 1). Default: an hour.
	AccessTTL func(n int) time.Duration
	// SIWCGrantScope, if set, is the scope a SIWC grant names in place of SIWCScope.
	SIWCGrantScope string

	mu        sync.Mutex
	codes     map[string]codeGrant // authorization code -> grant
	devices   map[string]*device   // device_auth_id -> device
	refresh   map[string]bool      // refresh token -> still valid
	access    map[string]string    // access token -> behaviour
	issued    int
	refreshes int
	requests  []Request
	siwc      []url.Values // SIWC authorize queries and token forms, in order
	hangs     map[string]chan struct{}
	block     chan struct{}
	started   chan struct{}
}

// Request is one model request the fake received.
type Request struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

type codeGrant struct{ challenge, redirect string }

type device struct {
	userCode, verifier, code string
	status                   string // pending, approved, denied
}

// New starts a fake; it stops when the test ends.
func New(t testing.TB) *Server {
	// The extension honours the fake's address in account settings only under this variable.
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	s := &Server{
		codes: map[string]codeGrant{}, devices: map[string]*device{}, refresh: map[string]bool{},
		access: map[string]string{}, hangs: map[string]chan struct{}{}, started: make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /api/accounts/deviceauth/usercode", s.userCode)
	mux.HandleFunc("POST /api/accounts/deviceauth/token", s.deviceToken)
	mux.HandleFunc("GET /api/accounts/authorize", s.siwcAuthorize)
	mux.HandleFunc("POST /api/accounts/oauth/token", s.siwcToken)
	mux.HandleFunc("POST /responses", s.responses)
	mux.HandleFunc("POST /backend-api/codex/responses", s.responses)
	mux.HandleFunc("GET /backend-api/wham/usage", s.usage)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// DefaultUsage is what the usage endpoint answers by default, in the shape of ChatGPT's own: a Plus
// plan with a 5-hour and a weekly window, code review's weekly window, one model's own limits, and
// a credit balance.
const DefaultUsage = `{
  "plan_type": "plus",
  "rate_limit": {
    "allowed": true, "limit_reached": false,
    "primary_window": {"used_percent": 42, "limit_window_seconds": 18000, "reset_after_seconds": 3600, "reset_at": 1794268800},
    "secondary_window": {"used_percent": 7.5, "limit_window_seconds": 604800, "reset_after_seconds": 400000, "reset_at": 1794668800}
  },
  "code_review_rate_limit": {
    "allowed": true, "limit_reached": false,
    "primary_window": {"used_percent": 0, "limit_window_seconds": 604800, "reset_after_seconds": 500000, "reset_at": 1794768800},
    "secondary_window": null
  },
  "additional_rate_limits": [{
    "limit_name": "GPT-5.3-Codex-Spark", "metered_feature": "codex_bengalfox",
    "rate_limit": {
      "allowed": true, "limit_reached": false,
      "primary_window": {"used_percent": 10, "limit_window_seconds": 18000, "reset_after_seconds": 100, "reset_at": 1794260000},
      "secondary_window": {"used_percent": 3, "limit_window_seconds": 604800, "reset_after_seconds": 200, "reset_at": 1794270000}
    }
  }],
  "credits": {"has_credits": true, "unlimited": false, "balance": "12.50"}
}`

// IssueToken returns an access token whose model requests act out behaviour: "text" (the conformance
// kit's reply, in two pieces), "rate_limited" (429 with Retry-After 20) or "auth_failed" (401).
func (s *Server) IssueToken(behaviour string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": random()})
	s.access[tok] = behaviour
	return tok
}

// IssueHang returns an access token whose model request starts a reply and then waits; the
// channel closes when the request is cancelled.
func (s *Server) IssueHang() (string, <-chan struct{}) {
	tok := s.IssueToken("hang")
	ch := make(chan struct{})
	s.mu.Lock()
	s.hangs[tok] = ch
	s.mu.Unlock()
	return tok, ch
}

// Grant signs in directly and returns the tokens a login would have given.
func (s *Server) Grant() (access, refresh, idToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.issueLocked()
	return t.AccessToken, t.RefreshToken, t.IDToken
}

// IDToken returns an ID token for the fake's account.
func IDToken() string {
	return jwt(map[string]any{"email": Email, "https://api.openai.com/auth": map[string]any{
		"chatgpt_account_id": AccountID, "chatgpt_plan_type": PlanType}})
}

// ApproveDevice approves the device sign-in showing userCode; DenyDevice refuses it.
func (s *Server) ApproveDevice(userCode string) { s.setDevice(userCode, "approved") }
func (s *Server) DenyDevice(userCode string)    { s.setDevice(userCode, "denied") }

func (s *Server) setDevice(userCode, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.devices {
		if d.userCode == userCode {
			d.status = status
		}
	}
}

// RevokeAccess makes an access token fail with 401 from now on.
func (s *Server) RevokeAccess(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access[tok] = "auth_failed"
}

// RefreshCalls returns how many refresh requests arrived.
func (s *Server) RefreshCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

// RefreshStarted closes when the first refresh request arrives.
func (s *Server) RefreshStarted() <-chan struct{} { return s.started }

// BlockRefresh holds refresh requests until release is called.
func (s *Server) BlockRefresh() (release func()) {
	ch := make(chan struct{})
	s.mu.Lock()
	s.block = ch
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// Requests returns the model requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// SIWCCalls returns the query of each SIWC authorize request and the form of each SIWC token
// request, in the order they came.
func (s *Server) SIWCCalls() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.siwc...)
}

// siwcAuthorize is the SIWC sign-in page: it checks the request as OpenAI would need it and
// sends the browser back with a code and the client ID it issued.
func (s *Server) siwcAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.siwc = append(s.siwc, q)
	s.mu.Unlock()
	if q.Get("client_id") != SIWCClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("agent_name_hint") == "" ||
		!strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") || q.Get("resource") != SIWCResource || q.Get("scope") != SIWCScope {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := url.Values{"state": {q.Get("state")}}
	if s.DenyBrowser {
		v.Set("error", "access_denied")
	} else {
		code := random()
		s.mu.Lock()
		s.codes[code] = codeGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
		s.mu.Unlock()
		v.Set("code", code)
		v.Set("client_id", SIWCIssuedClientID)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// siwcToken is the SIWC token endpoint: the code exchange and renewal of the issued client, for
// the API resource; refresh tokens rotate as on the Codex token endpoint.
func (s *Server) siwcToken(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_request"})
		return
	}
	f := r.PostForm
	s.mu.Lock()
	s.siwc = append(s.siwc, f)
	s.mu.Unlock()
	if f.Get("client_id") != SIWCIssuedClientID {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}
	if f.Get("resource") != SIWCResource {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_target"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Get("grant_type") {
	case "authorization_code":
		g, ok := s.codes[f.Get("code")]
		delete(s.codes, f.Get("code"))
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || g.redirect != f.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
	case "refresh_token":
		s.refreshes++
		if s.RefreshFailure == "unavailable" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "temporarily_unavailable"})
			return
		}
		valid, known := s.refresh[f.Get("refresh_token")]
		if !known || !valid {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		s.refresh[f.Get("refresh_token")] = false
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
		return
	}
	t := s.issueLocked()
	t.Scope = SIWCScope
	if s.SIWCGrantScope != "" {
		t.Scope = s.SIWCGrantScope
	}
	if f.Get("grant_type") == "refresh_token" {
		t.IDToken = "" // a renewal need not send one
	}
	writeJSON(w, http.StatusOK, t)
}

// SIWCGrant signs in with ChatGPT directly and returns the tokens a SIWC login would have given.
func (s *Server) SIWCGrant() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.issueLocked()
	return t.AccessToken, t.RefreshToken
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		q.Get("code_challenge") == "" || q.Get("state") == "" || !strings.Contains(q.Get("scope"), "offline_access") {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := url.Values{"state": {q.Get("state")}}
	if s.DenyBrowser {
		v.Set("error", "access_denied")
	} else {
		code := random()
		s.mu.Lock()
		s.codes[code] = codeGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
		s.mu.Unlock()
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) userCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ClientID string `json:"client_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ClientID != ClientID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_client"}})
		return
	}
	d := &device{userCode: strings.ToUpper(random()[:8]), verifier: random(), code: random(), status: "pending"}
	id := "dev-" + random()
	s.mu.Lock()
	s.devices[id] = d
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"device_auth_id": id, "user_code": d.userCode, "interval": "1"})
}

// deviceToken answers a poll: 403 while the code has not been entered, the authorization code and
// its PKCE verifier once approved, 400 when denied.
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	d := s.devices[in.DeviceAuthID]
	if d == nil || d.userCode != in.UserCode {
		s.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_request"}})
		return
	}
	status := d.status
	if status == "approved" {
		sum := sha256.Sum256([]byte(d.verifier))
		s.codes[d.code] = codeGrant{challenge: base64.RawURLEncoding.EncodeToString(sum[:]), redirect: "https://auth.openai.com/deviceauth/callback"}
	}
	s.mu.Unlock()
	switch status {
	case "pending":
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{"code": "deviceauth_authorization_pending"}})
	case "denied":
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "access_denied"}})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"authorization_code": d.code, "code_verifier": d.verifier, "code_challenge": "unused"})
	}
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil || r.PostForm.Get("client_id") != ClientID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_client"})
		return
	}
	f := r.PostForm
	switch f.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		g, ok := s.codes[f.Get("code")]
		delete(s.codes, f.Get("code"))
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || g.redirect != f.Get("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			s.mu.Unlock()
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_grant"})
			return
		}
		t := s.issueLocked()
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, t)
	case "refresh_token":
		s.mu.Lock()
		s.refreshes++
		if s.refreshes == 1 {
			close(s.started)
		}
		block := s.block
		s.mu.Unlock()
		if block != nil {
			<-block
		}
		if s.RefreshFailure == "unavailable" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "temporarily_unavailable"})
			return
		}
		if f.Get("scope") == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_scope"})
			return
		}
		s.mu.Lock()
		valid, known := s.refresh[f.Get("refresh_token")]
		if !known || !valid {
			s.mu.Unlock()
			code := "invalid_grant"
			if known {
				code = "refresh_token_reused"
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": code, "message": "refresh refused"}})
			return
		}
		s.refresh[f.Get("refresh_token")] = false
		t := s.issueLocked()
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, t)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unsupported_grant_type"})
	}
}

func (s *Server) issueLocked() tokens {
	s.issued++
	ttl := time.Hour
	if s.AccessTTL != nil {
		ttl = s.AccessTTL(s.issued)
	}
	t := tokens{
		AccessToken:  jwt(map[string]any{"exp": time.Now().Add(ttl).Unix(), "sub": random()}),
		RefreshToken: "rt-" + random(),
		IDToken:      IDToken(),
		TokenType:    "Bearer",
		ExpiresIn:    int64(ttl.Seconds()),
	}
	s.access[t.AccessToken] = "text"
	s.refresh[t.RefreshToken] = true
	return t
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: r.URL.Path, Header: r.Header.Clone(), Body: body})
	behaviour := s.access[tok]
	hang := s.hangs[tok]
	s.mu.Unlock()
	switch behaviour {
	case "text", "hang":
	case "rate_limited":
		w.Header().Set("Retry-After", "20")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"type": "rate_limit_error", "code": "rate_limit_exceeded", "message": "Rate limit reached"}})
		return
	default:
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"type": "invalid_request_error", "code": "token_expired", "message": "token expired"}})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flush := func() {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	model, _ := body["model"].(string)
	resp := map[string]any{"id": "resp_codextest", "object": "response", "created_at": 1700000000, "status": "in_progress", "model": model, "output": []any{}}
	ev := func(typ string, fields map[string]any) {
		fields["type"] = typ
		b, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
		flush()
	}
	ev("response.created", map[string]any{"response": resp})
	ev("response.output_item.added", map[string]any{"output_index": 0, "item": map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
	pieces := []string{"Hello", " from the conformance kit."}
	ev("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_1", "delta": pieces[0]})
	if hang != nil {
		<-r.Context().Done()
		close(hang)
		return
	}
	ev("response.output_text.delta", map[string]any{"output_index": 0, "content_index": 0, "item_id": "msg_1", "delta": pieces[1]})
	item := map[string]any{"id": "msg_1", "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": conformance.ReplyText, "annotations": []any{}}}}
	ev("response.output_item.done", map[string]any{"output_index": 0, "item": item})
	resp["status"], resp["output"] = "completed", []any{item}
	resp["usage"] = map[string]any{"input_tokens": conformance.InputTokens, "output_tokens": conformance.OutputTokens, "total_tokens": conformance.InputTokens + conformance.OutputTokens}
	ev("response.completed", map[string]any{"response": resp})
}

// usage answers the usage endpoint for an access token the fake issued; like the responses endpoint
// it acts out the token's behaviour (401 for an unknown token, 429 for "rate_limited"). The request
// is recorded with the model requests.
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: r.URL.Path, Header: r.Header.Clone()})
	behaviour := s.access[tok]
	body := s.Usage
	s.mu.Unlock()
	switch behaviour {
	case "text", "hang":
	case "rate_limited":
		w.Header().Set("Retry-After", "20")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": map[string]string{"type": "rate_limit_error", "message": "Rate limit reached"}})
		return
	default:
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "token expired"}})
		return
	}
	if body == "" {
		body = DefaultUsage
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(b) + ".codextest"
}

func random() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
