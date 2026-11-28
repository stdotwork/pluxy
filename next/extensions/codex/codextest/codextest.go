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

const ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const (
	SIWCClientID       = "dynamic_agent_client"
	SIWCIssuedClientID = "siwc-client-codextest"
	SIWCResource       = "https://api.openai.com/v1"
	SIWCScope          = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

const (
	AccountID = "acct-codextest"
	Email     = "codex@example.invalid"
	PlanType  = "plus"
)

type Server struct {
	*httptest.Server

	DenyBrowser    bool
	RefreshFailure string
	Usage          string
	Subscriptions  string
	ResetCredits   string
	AccessTTL      func(n int) time.Duration
	SIWCGrantScope string

	mu          sync.Mutex
	codes       map[string]codeGrant
	devices     map[string]*device
	refresh     map[string]bool
	access      map[string]string
	issued      int
	refreshes   int
	requests    []Request
	listings    int
	listHeaders []http.Header
	siwc        []url.Values
	hangs       map[string]chan struct{}
	block       chan struct{}
	started     chan struct{}
}

type Request struct {
	Path   string
	Header http.Header
	Body   map[string]any
}

type codeGrant struct{ challenge, redirect string }

type device struct {
	userCode, verifier, code string
	status                   string
}

func New(t testing.TB) *Server {
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
	mux.HandleFunc("GET /models", s.models)
	mux.HandleFunc("GET /backend-api/codex/models", s.models)
	mux.HandleFunc("GET /backend-api/wham/usage", s.usage)
	mux.HandleFunc("GET /backend-api/subscriptions", s.quotaExtra)
	mux.HandleFunc("GET /backend-api/wham/rate-limit-reset-credits", s.quotaExtra)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

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

func (s *Server) IssueToken(behaviour string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tok := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": random()})
	s.access[tok] = behaviour
	return tok
}

func (s *Server) IssueHang() (string, <-chan struct{}) {
	tok := s.IssueToken("hang")
	ch := make(chan struct{})
	s.mu.Lock()
	s.hangs[tok] = ch
	s.mu.Unlock()
	return tok, ch
}

func (s *Server) Grant() (access, refresh, idToken string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.issueLocked()
	return t.AccessToken, t.RefreshToken, t.IDToken
}

func IDToken() string {
	return jwt(map[string]any{"email": Email, "https://api.openai.com/auth": map[string]any{
		"chatgpt_account_id": AccountID, "chatgpt_plan_type": PlanType}})
}

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

func (s *Server) RefreshCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

func (s *Server) RefreshStarted() <-chan struct{} { return s.started }

func (s *Server) BlockRefresh() (release func()) {
	ch := make(chan struct{})
	s.mu.Lock()
	s.block = ch
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

func (s *Server) ModelListRequests() (int, []http.Header) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listings, append([]http.Header(nil), s.listHeaders...)
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) SIWCCalls() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.siwc...)
}

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
		t.IDToken = ""
	}
	writeJSON(w, http.StatusOK, t)
}

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
	case "account_closed":
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"type": "invalid_request_error", "code": "account_deactivated", "message": "Your account has been deactivated. Please contact support."}})
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

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.listings++
	s.listHeaders = append(s.listHeaders, r.Header.Clone())
	_, known := s.access[tok]
	s.mu.Unlock()
	if !known {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "Incorrect API key provided", "type": "invalid_request_error", "code": "invalid_api_key"}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{map[string]any{"id": "gpt-codextest", "object": "model", "owned_by": "codextest"}}})
}

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

func (s *Server) quotaExtra(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.requests = append(s.requests, Request{Path: r.URL.RequestURI(), Header: r.Header.Clone()})
	behaviour := s.access[tok]
	body := s.ResetCredits
	if r.URL.Path == "/backend-api/subscriptions" {
		body = s.Subscriptions
	}
	s.mu.Unlock()
	if behaviour != "text" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if body == "" {
		http.NotFound(w, r)
		return
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
