package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// fixedSource always hands out one generation.
type fixedSource struct{ g *Generation }

func (s fixedSource) Acquire() (*Generation, func()) { return s.g, func() {} }

// events is a Stream that returns its events in turn, then keeps returning the last.
type events struct{ evs []llm.Event }

func (s *events) Next() llm.Event {
	ev := s.evs[0]
	if len(s.evs) > 1 {
		s.evs = s.evs[1:]
	}
	return ev
}
func (s *events) Close() {}

// testHandler returns the client API handler for cfg with one account, "work", whose attempts
// return the events reply returns for each request.
func testHandler(t *testing.T, cfg *Config, reply func(req llm.Request) []llm.Event) http.Handler {
	t.Helper()
	if err := cfg.CORS.prepare(); err != nil {
		t.Fatal(err)
	}
	attempt := func(_ context.Context, _ string, _ protocol.Account, req llm.Request) route.Stream {
		return &events{evs: reply(req)}
	}
	router, err := route.New(route.Config{}, []route.Account{{ID: "work"}}, attempt, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(fixedSource{&Generation{Config: cfg, Router: router, Attempt: attempt}})
}

// hello is a short Chat reply.
func hello(llm.Request) []llm.Event {
	return []llm.Event{
		{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r1", Model: "m"}},
		{Type: llm.EventItemStart, Index: 0, Item: &llm.ItemStart{Kind: llm.ItemMessage}},
		{Type: llm.EventTextDelta, Index: 0, Delta: "hi"},
		{Type: llm.EventItemEnd, Index: 0},
		{Type: llm.EventFinish, Finish: &llm.Finish{Reason: llm.FinishStop}},
	}
}

const chatBody = `{"model":"m","messages":[{"role":"user","content":"hello"}]}`

func preflight(origin string) *http.Request {
	r := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "x-custom-header, content-type")
	return r
}

// Without allowed origins core sends no CORS headers and does not answer preflight requests:
// today's core allowed every origin, which this core does not carry over.
func TestCORSOffByDefault(t *testing.T) {
	h := testHandler(t, &Config{}, hello)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, preflight("https://app.example.test"))
	if w.Code == http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight answered %d with %v, want no CORS", w.Code, w.Header())
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
	r.Header.Set("Origin", "https://app.example.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("request answered %d with %v, want 200 without CORS headers", w.Code, w.Header())
	}
}

// A listed origin gets its preflight answered before the client key is checked, and its own
// origin back (not "*"); an origin not listed gets nothing.
func TestCORSListedOrigin(t *testing.T) {
	h := testHandler(t, &Config{ClientKeys: []string{"k"}, CORS: CORSConfig{AllowedOrigins: []string{"HTTPS://App.Example.test"}}}, hello)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, preflight("https://app.example.test"))
	hd := w.Header()
	if w.Code != http.StatusNoContent || hd.Get("Access-Control-Allow-Origin") != "https://app.example.test" || hd.Get("Vary") != "Origin" {
		t.Fatalf("preflight answered %d with %v", w.Code, hd)
	}
	if got, want := hd.Get("Access-Control-Allow-Headers"), "Authorization, Content-Type, X-Api-Key, X-Goog-Api-Key, Anthropic-Version, Anthropic-Beta, X-Custom-Header"; got != want {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, want)
	}
	if got := hd.Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
		t.Errorf("Access-Control-Allow-Methods = %q", got)
	}
	if got := hd.Get("Access-Control-Expose-Headers"); got != "X-CPA-TRACE-ID, Retry-After" {
		t.Errorf("Access-Control-Expose-Headers = %q", got)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, preflight("https://other.example.test"))
	if w.Code == http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("an origin not listed got %d with %v", w.Code, w.Header())
	}

	// A request from the listed origin still needs the client key.
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
	r.Header.Set("Origin", "https://app.example.test")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || w.Header().Get("Access-Control-Allow-Origin") != "https://app.example.test" {
		t.Errorf("request without a key answered %d with %v, want 401 with CORS headers", w.Code, w.Header())
	}
}

// "*" allows every origin, as today's core does, and the headers go on every reply.
func TestCORSEveryOrigin(t *testing.T) {
	h := testHandler(t, &Config{CORS: CORSConfig{AllowedOrigins: []string{"*"}}}, hello)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody)))
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Vary") != "" {
		t.Errorf("answered %d with %v", w.Code, w.Header())
	}
}

func TestCORSConfigChecksOrigins(t *testing.T) {
	for _, o := range []string{"app.example.test", "https://app.example.test/path", "ftp://app.example.test", "https://u@app.example.test", "https://app.example.test?x=1"} {
		c := CORSConfig{AllowedOrigins: []string{o}}
		if err := c.prepare(); err == nil {
			t.Errorf("origin %q accepted", o)
		}
	}
	c := CORSConfig{AllowedOrigins: []string{"*", "http://LOCALHOST:5173/"}}
	if err := c.prepare(); err != nil || c.AllowedOrigins[1] != "http://localhost:5173" {
		t.Errorf("prepare = %v, %v", c.AllowedOrigins, err)
	}
}

// A request that reached an account gets a trace ID, which the log lines about it carry with
// the account; one refused before that gets none.
func TestTraceHeader(t *testing.T) {
	out := captureLog(t)
	fail := func(llm.Request) []llm.Event {
		return []llm.Event{{Type: llm.EventError, Error: &llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Message: "bad request"}}}
	}
	h := testHandler(t, &Config{ClientKeys: []string{"k"}}, fail)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	id := w.Header().Get(TraceHeader)
	if w.Code != http.StatusBadRequest || !regexp.MustCompile(`^\d{14}-[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("answered %d with trace %q", w.Code, id)
	}
	if !strings.Contains(out.String(), "[trace "+id+" account work]: invalid_request: bad request") {
		t.Errorf("log = %q, want the trace ID and account", out.String())
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody)))
	if w.Code != http.StatusUnauthorized || w.Header().Get(TraceHeader) != "" {
		t.Errorf("a request without a key answered %d with trace %q", w.Code, w.Header().Get(TraceHeader))
	}
}
