package claudetest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

const ClientID = "claudetest-client"

const Email = "user@claudetest.example"

const (
	AccountUUID      = "11111111-1111-4111-8111-111111111111"
	OrganizationUUID = "22222222-2222-4222-8222-222222222222"
)

const (
	DefaultUsage = `{
  "five_hour": {"utilization": 37.0, "resets_at": "2026-11-09T15:00:00.123456+00:00"},
  "seven_day": {"utilization": 12.5, "resets_at": "2026-11-14T09:00:00+00:00"},
  "seven_day_oauth_apps": null,
  "seven_day_opus": null,
  "seven_day_sonnet": {"utilization": 0.0, "resets_at": "2026-11-14T09:00:00+00:00"},
  "extra_usage": {"is_enabled": true, "monthly_limit": 5000, "used_credits": 1250.0, "utilization": 25.0, "currency": "USD"}
}`
	DefaultProfile = `{
  "account": {"uuid": "` + AccountUUID + `", "email": "` + Email + `", "has_claude_max": true, "has_claude_pro": false},
  "organization": {"uuid": "` + OrganizationUUID + `", "organization_type": "claude_max", "rate_limit_tier": "default_claude_max_5x", "subscription_status": "active"}
}`
)

const TokenLifetime = 8 * 3600

type Request struct {
	Prefix string
	Header http.Header
	Body   []byte
}

type Server struct {
	*httptest.Server

	Messages       func(w http.ResponseWriter, r *http.Request, req Request)
	Usage, Profile string
	ProfileStatus  int
	DenySignIn     bool
	TokenDown      bool

	mu       sync.Mutex
	n        int
	codes    map[string]codeGrant
	refresh  map[string]bool
	access   map[string]bool
	apiKeys  map[string]bool
	requests []Request
	renewals int
	listings int
	oauth    []Request
}

type codeGrant struct{ challenge, redirect, state string }

func New(t testing.TB) *Server {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	s := &Server{codes: map[string]codeGrant{}, refresh: map[string]bool{}, access: map[string]bool{}, apiKeys: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Settings(prefix string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{
		"base_url":      s.URL + prefix,
		"authorize_url": s.URL + "/oauth/authorize",
		"token_url":     s.URL + "/v1/oauth/token",
		"client_id":     ClientID,
	})
	return b
}

func (s *Server) IssueKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	k := fmt.Sprintf("sk-ant-api-claudetest-%d", s.n)
	s.apiKeys[k] = true
	return k
}

func (s *Server) SetupToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	t := fmt.Sprintf("sk-ant-oat01-claudetest-%d", s.n)
	s.access[t] = true
	return t
}

func (s *Server) Grant() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grantLocked()
}

func (s *Server) grantLocked() (string, string) {
	s.n++
	a, r := fmt.Sprintf("claudetest-access-%d", s.n), fmt.Sprintf("claudetest-refresh-%d", s.n)
	s.access[a], s.refresh[r] = true, true
	return a, r
}

func (s *Server) Revoke(access string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.access, access)
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) ModelRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listings
}

func (s *Server) OAuthRequests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.oauth...)
}

func (s *Server) Renewals() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewals
}

func SignIn(t testing.TB, authURL string) string {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatalf("opening the sign-in URL: %v", err)
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusFound || loc == "" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("the sign-in URL answered %d without a redirect: %s", resp.StatusCode, body)
	}
	return loc
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case p == "/oauth/authorize" && r.Method == http.MethodGet:
		s.authorize(w, r)
	case p == "/v1/oauth/token" && r.Method == http.MethodPost:
		s.token(w, r)
	case (p == "/api/oauth/usage" || p == "/api/oauth/profile") && r.Method == http.MethodGet:
		s.account(w, r)
	case strings.HasSuffix(p, "/v1/models") && r.Method == http.MethodGet:
		s.models(w, r)
	case strings.HasSuffix(p, "/v1/messages") && r.Method == http.MethodPost:
		s.messages(w, r, strings.TrimSuffix(p, "/v1/messages"))
	default:
		apiError(w, http.StatusNotFound, "not_found_error", "no such endpoint: "+p)
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Host == "" || q.Get("client_id") != ClientID || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	back := url.Values{"state": {q.Get("state")}}
	if s.DenySignIn {
		back.Set("error", "access_denied")
	} else {
		s.mu.Lock()
		s.n++
		code := fmt.Sprintf("claudetest-code-%d", s.n)
		s.codes[code] = codeGrant{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), state: q.Get("state")}
		s.mu.Unlock()
		back.Set("code", code)
	}
	redirect.RawQuery = back.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.TokenDown {
		http.Error(w, `{"error":"temporarily_unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || r.Header.Get("Content-Type") != "application/json" {
		oauthError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if in["client_id"] != ClientID {
		oauthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch in["grant_type"] {
	case "authorization_code":
		g, ok := s.codes[in["code"]]
		delete(s.codes, in["code"])
		sum := sha256.Sum256([]byte(in["code_verifier"]))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge || in["redirect_uri"] != g.redirect || in["state"] != g.state {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
	case "refresh_token":
		if !s.refresh[in["refresh_token"]] {
			oauthError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		delete(s.refresh, in["refresh_token"])
		s.renewals++
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	a, rt := s.grantLocked()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"token_type": "Bearer", "access_token": a, "refresh_token": rt, "expires_in": TokenLifetime,
		"account":      map[string]any{"uuid": AccountUUID, "email_address": Email},
		"organization": map[string]any{"uuid": OrganizationUUID, "name": "claudetest"},
	})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.listings++
	ok := s.apiKeys[r.Header.Get("X-Api-Key")]
	s.mu.Unlock()
	if !ok {
		apiError(w, http.StatusUnauthorized, "authentication_error", "invalid x-api-key")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"data":[{"type":"model","id":"claude-test","display_name":"Claude Test"}],"has_more":false}`)
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request, prefix string) {
	body, _ := io.ReadAll(r.Body)
	req := Request{Prefix: prefix, Header: r.Header.Clone(), Body: body}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	ok := s.apiKeys[r.Header.Get("X-Api-Key")]
	if bearer, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		ok = s.access[bearer] && strings.Contains(r.Header.Get("Anthropic-Beta"), "oauth-2025-04-20")
	}
	s.mu.Unlock()
	if !ok {
		apiError(w, http.StatusUnauthorized, "authentication_error", "invalid credential")
		return
	}
	if s.Messages != nil {
		s.Messages(w, r, req)
		return
	}
	var in struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &in)
	if in.Stream {
		StreamText(w, "Hel", "lo")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"msg_claudetest","type":"message","role":"assistant","model":"claude-test",`+
		`"content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,`+
		`"usage":{"input_tokens":11,"output_tokens":7}}`)
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.oauth = append(s.oauth, Request{Prefix: r.URL.Path, Header: r.Header.Clone()})
	bearer, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	ok := found && s.access[bearer] && strings.Contains(r.Header.Get("Anthropic-Beta"), "oauth-2025-04-20")
	usage, profile, status := s.Usage, s.Profile, s.ProfileStatus
	s.mu.Unlock()
	if !ok {
		apiError(w, http.StatusUnauthorized, "authentication_error", "invalid bearer token")
		return
	}
	body, fail := usage, 0
	if body == "" {
		body = DefaultUsage
	}
	if r.URL.Path == "/api/oauth/profile" {
		body, fail = profile, status
		if body == "" {
			body = DefaultProfile
		}
	}
	if fail != 0 {
		apiError(w, fail, "api_error", "profile unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, body)
}

func StreamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	event := func(typ, data string) {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		if f != nil {
			f.Flush()
		}
	}
	event("message_start", `{"type":"message_start","message":{"id":"msg_claudetest","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":1}}}`)
	event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	for _, p := range pieces {
		b, _ := json.Marshal(p)
		event("content_block_delta", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, b))
	}
	event("content_block_stop", `{"type":"content_block_stop","index":0}`)
	event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}`)
	event("message_stop", `{"type":"message_stop"}`)
}

func apiError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
	w.Write(b)
}

func oauthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, code)
}
