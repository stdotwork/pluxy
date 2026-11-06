// Package kimitest is a fake Kimi for tests of the Kimi extension: the device-code login and token
// endpoint at /api/oauth/*, and the Kimi Code API (Chat Completions, Responses, Messages) under
// /coding/v1. It issues made-up tokens and never talks to Kimi.
package kimitest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request is an API request the server answered with a valid token.
type Request struct {
	Path   string
	Header http.Header
	Body   []byte
}

// Server is the fake. Set its fields before requests arrive.
type Server struct {
	*httptest.Server

	// Generate, if set, answers API requests whose token is valid. The default replies "Hello"
	// as a Chat completion (streamed when asked) with usage 11 in, 7 out.
	Generate func(w http.ResponseWriter, r *http.Request, req Request)
	// Deny makes every device login end with access_denied once polled.
	Deny bool
	// TokenDown makes the token endpoint answer 503.
	TokenDown bool

	mu       sync.Mutex
	n        int
	codes    map[string]*code // device code to its state
	access   map[string]bool
	refresh  map[string]bool
	requests []Request
}

type code struct {
	user     string
	approved bool
	deviceID string
}

// New starts a fake that is closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{codes: map[string]*code{}, access: map[string]bool{}, refresh: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Settings returns account settings that point the extension at the server.
func (s *Server) Settings() json.RawMessage {
	b, _ := json.Marshal(map[string]string{"base_url": s.URL + "/coding", "auth_base_url": s.URL})
	return b
}

// Grant returns a new access and refresh token the server accepts.
func (s *Server) Grant() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grant()
}

func (s *Server) grant() (string, string) {
	s.n++
	a, r := fmt.Sprintf("kimitest-access-%d", s.n), fmt.Sprintf("kimitest-refresh-%d", s.n)
	s.access[a], s.refresh[r] = true, true
	return a, r
}

// Revoke makes the server refuse a token it issued.
func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.access, token)
	delete(s.refresh, token)
}

// Approve approves the device login showing userCode, as the user would on Kimi's page.
func (s *Server) Approve(userCode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.codes {
		if c.user == userCode {
			c.approved = true
			return true
		}
	}
	return false
}

// Requests returns the API requests the server answered, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/oauth/device_authorization" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		s.mu.Lock()
		s.n++
		dc, uc := fmt.Sprintf("kimitest-device-%d", s.n), fmt.Sprintf("KIMI-%04d", s.n)
		s.codes[dc] = &code{user: uc, deviceID: r.Header.Get("X-Msh-Device-Id")}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"device_code": dc, "user_code": uc, "verification_uri": s.URL + "/device",
			"verification_uri_complete": s.URL + "/device?user_code=" + uc, "expires_in": 600, "interval": 1})
	case r.URL.Path == "/api/oauth/token" && r.Method == http.MethodPost:
		s.token(w, r)
	case strings.HasPrefix(r.URL.Path, "/coding/v1/") && r.Method == http.MethodPost:
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		ok := s.access[tok]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "Invalid Authentication", "type": "invalid_authentication_error"}})
			return
		}
		req := Request{Path: r.URL.Path, Header: r.Header.Clone()}
		req.Body, _ = io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		if s.Generate != nil {
			s.Generate(w, r, req)
			return
		}
		var in struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(req.Body, &in)
		if in.Stream {
			StreamText(w, "Hel", "lo")
			return
		}
		JSONText(w, "Hello")
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.TokenDown {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.PostForm.Get("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		c := s.codes[r.PostForm.Get("device_code")]
		switch {
		case c == nil:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		case s.Deny:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "access_denied"})
		case !c.approved:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "authorization_pending"})
		default:
			delete(s.codes, r.PostForm.Get("device_code"))
			a, rt := s.grant()
			writeJSON(w, http.StatusOK, map[string]any{"access_token": a, "refresh_token": rt, "expires_in": 3600, "token_type": "Bearer"})
		}
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		if !s.refresh[rt] {
			// Kimi refuses a refresh token with 401, not invalid_grant.
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		delete(s.refresh, rt)
		a, nrt := s.grant()
		writeJSON(w, http.StatusOK, map[string]any{"access_token": a, "refresh_token": nrt, "expires_in": 3600, "token_type": "Bearer"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Usage is the usage the default replies report.
const Usage = `{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`

// StreamText writes a streamed Chat completion: one chunk per piece, then the finish reason and
// usage, then [DONE].
func StreamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for i, p := range pieces {
		t, _ := json.Marshal(p)
		role := ""
		if i == 0 {
			role = `"role":"assistant",`
		}
		fmt.Fprintf(w, "data: {\"id\":\"kimitest\",\"object\":\"chat.completion.chunk\",\"model\":\"k3\",\"choices\":[{\"index\":0,\"delta\":{%s\"content\":%s},\"finish_reason\":null}]}\n\n", role, t)
	}
	fmt.Fprintf(w, "data: {\"id\":\"kimitest\",\"object\":\"chat.completion.chunk\",\"model\":\"k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":%s}\n\n", Usage)
	io.WriteString(w, "data: [DONE]\n\n")
}

// JSONText writes a Chat completion holding text.
func JSONText(w http.ResponseWriter, text string) {
	t, _ := json.Marshal(text)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":"kimitest","object":"chat.completion","model":"k3","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":%s}`, t, Usage)
}
