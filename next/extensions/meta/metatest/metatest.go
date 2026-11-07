// Package metatest is a fake Meta for tests of the Meta extension: the device-code login at
// /oidc/device/*, the key endpoint at /muse-code/key, and the Responses API at /v1/responses. It
// issues made-up tokens and keys and never talks to Meta.
package metatest

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

// Email is the e-mail address the key endpoint names.
const Email = "user@example.com"

// Request is a Responses request the server answered with a valid key.
type Request struct {
	Header http.Header
	Body   []byte
}

// Server is the fake. Set its fields before requests arrive.
type Server struct {
	*httptest.Server

	// Generate, if set, answers Responses requests whose key is valid. The default streams
	// "Hello" with usage 11 in, 7 out.
	Generate func(w http.ResponseWriter, r *http.Request, req Request)
	// Deny makes every device login end with access_denied once polled.
	Deny bool
	// MintDown makes the key endpoint answer 503.
	MintDown bool
	// Answer, if set, sees every request first; it answers one by returning true.
	Answer func(w http.ResponseWriter, r *http.Request) bool

	mu       sync.Mutex
	n        int
	codes    map[string]*code
	dca      map[string]bool
	keys     map[string]bool
	requests []Request
}

type code struct {
	user     string
	approved bool
}

// New starts a fake that is closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{codes: map[string]*code{}, dca: map[string]bool{}, keys: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Settings returns account settings that point the extension at the server.
func (s *Server) Settings() json.RawMessage {
	b, _ := json.Marshal(map[string]string{"base_url": s.URL + "/v1", "auth_base_url": s.URL, "mint_url": s.URL + "/muse-code/key"})
	return b
}

// Grant returns a new device token and an API key minted from it.
func (s *Server) Grant() (dca, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	dca, key = fmt.Sprintf("dca:metatest-%d", s.n), fmt.Sprintf("metatest-key-%d", s.n)
	s.dca[dca], s.keys[key] = true, true
	return dca, key
}

// Revoke makes the server refuse a key or device token it issued.
func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, token)
	delete(s.dca, token)
}

// Approve approves the device login showing userCode, as the user would on Meta's page.
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

// Requests returns the Responses requests the server answered, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.Answer != nil && s.Answer(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/oidc/device/authorization/" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		s.mu.Lock()
		s.n++
		dc, uc := fmt.Sprintf("metatest-device-%d", s.n), fmt.Sprintf("META-%04d", s.n)
		s.codes[dc] = &code{user: uc}
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"device_code": dc, "user_code": uc, "verification_uri": s.URL + "/device",
			"verification_uri_complete": s.URL + "/device?user_code=" + uc, "expires_in": 600, "interval": 1})
	case r.URL.Path == "/oidc/device/token/" && r.Method == http.MethodPost:
		_ = r.ParseForm()
		s.mu.Lock()
		defer s.mu.Unlock()
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
			s.n++
			dca := fmt.Sprintf("dca:metatest-%d", s.n)
			s.dca[dca] = true
			writeJSON(w, http.StatusOK, map[string]any{"access_token": dca, "token_type": "Bearer", "expires_in": 7776000})
		}
	case r.URL.Path == "/muse-code/key" && r.Method == http.MethodPost:
		if s.MintDown {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.dca[tok] {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "Invalid device token", "type": "invalid_request_error"}})
			return
		}
		s.n++
		key := fmt.Sprintf("metatest-key-%d", s.n)
		s.keys[key] = true
		writeJSON(w, http.StatusOK, map[string]any{"api_key": key, "base_url": s.URL + "/v1", "user_email": Email, "subs_tier_name": "Muse Code", "is_subs_active": true})
	case r.URL.Path == "/v1/responses" && r.Method == http.MethodPost:
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		s.mu.Lock()
		ok := s.keys[tok]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"message": "Invalid API key", "type": "invalid_request_error"}})
			return
		}
		req := Request{Header: r.Header.Clone()}
		req.Body, _ = io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		if s.Generate != nil {
			s.Generate(w, r, req)
			return
		}
		StreamText(w, "Hel", "lo")
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// StreamText writes a Responses stream: the response's start, one text delta per piece, Meta's
// subscription usage event, and the completed response with usage.
func StreamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	ev := func(name string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
	}
	resp := map[string]any{"id": "resp_metatest", "object": "response", "model": "muse-spark-1.3", "status": "in_progress", "output": []any{}}
	ev("response.created", map[string]any{"type": "response.created", "response": resp})
	ev("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"id": "msg_metatest", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
	text := ""
	for _, p := range pieces {
		text += p
		ev("response.output_text.delta", map[string]any{"type": "response.output_text.delta", "item_id": "msg_metatest", "output_index": 0, "content_index": 0, "delta": p})
	}
	ev("response.subscription_usage", map[string]any{"type": "response.subscription_usage", "subscription": map[string]any{"used": 1}})
	item := map[string]any{"id": "msg_metatest", "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
	ev("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
	done := map[string]any{"id": "resp_metatest", "object": "response", "model": "muse-spark-1.3", "status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18}}
	ev("response.completed", map[string]any{"type": "response.completed", "response": done})
}
