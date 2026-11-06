// Package xaitest is a fake xAI for tests of the xAI extension: its Responses API under /v1/responses.
// It accepts only the API keys it issued and never talks to xAI.
package xaitest

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

// Request is an API request the server answered with a valid key.
type Request struct {
	Path   string
	Header http.Header
	Body   []byte
}

// Server is the fake. Set its fields before requests arrive.
type Server struct {
	*httptest.Server

	// Generate, if set, answers requests whose key is valid. The default replies "Hello" as a
	// Responses message (streamed when asked) with usage 11 in, 7 out.
	Generate func(w http.ResponseWriter, r *http.Request, req Request)

	mu       sync.Mutex
	n        int
	keys     map[string]bool
	requests []Request
}

// New starts a fake that is closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{keys: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Settings returns account settings that point the extension at the server.
func (s *Server) Settings() json.RawMessage {
	b, _ := json.Marshal(map[string]string{"base_url": s.URL + "/v1"})
	return b
}

// Key issues a new API key the server accepts.
func (s *Server) Key() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	k := fmt.Sprintf("xaitest-key-%d", s.n)
	s.keys[k] = true
	return k
}

// Revoke makes the server refuse a key it issued.
func (s *Server) Revoke(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.keys, key)
}

// Requests returns the API requests the server answered, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	ok := s.keys[key]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "Client specified an invalid argument", "error": "Incorrect API key provided."})
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
}

// Usage is the usage the default replies report.
var Usage = map[string]int{"input_tokens": 11, "output_tokens": 7, "total_tokens": 18}

// StreamText writes a streamed Responses reply holding one message of text, one delta per piece.
func StreamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	resp := map[string]any{"id": "resp_xaitest", "object": "response", "created_at": 1700000000, "status": "in_progress", "model": "grok-test", "output": []any{}, "usage": nil}
	event(w, "response.created", map[string]any{"type": "response.created", "sequence_number": 0, "response": resp})
	event(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "sequence_number": 1, "output_index": 0,
		"item": map[string]any{"id": "msg_xaitest", "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}})
	text := ""
	for i, p := range pieces {
		text += p
		event(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "sequence_number": 2 + i, "item_id": "msg_xaitest",
			"output_index": 0, "content_index": 0, "delta": p})
	}
	item := messageItem(text)
	event(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "sequence_number": 2 + len(pieces), "output_index": 0, "item": item})
	resp["status"], resp["output"], resp["usage"] = "completed", []any{item}, Usage
	event(w, "response.completed", map[string]any{"type": "response.completed", "sequence_number": 3 + len(pieces), "response": resp})
}

// JSONText writes a Responses reply holding one message of text.
func JSONText(w http.ResponseWriter, text string) {
	writeJSON(w, http.StatusOK, map[string]any{"id": "resp_xaitest", "object": "response", "created_at": 1700000000, "status": "completed",
		"model": "grok-test", "output": []any{messageItem(text)}, "usage": Usage})
}

func messageItem(text string) map[string]any {
	return map[string]any{"id": "msg_xaitest", "type": "message", "status": "completed", "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func event(w io.Writer, name string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
