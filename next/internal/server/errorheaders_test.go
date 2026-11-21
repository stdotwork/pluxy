package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/stdotwork/pluxy/next/llm"
)

// A client told the upstream is overloaded or unavailable also learns when to try again.
func TestRetryAfterOnUnavailable(t *testing.T) {
	formats := map[string]clientFormat{"chat": chatFormat, "responses": responsesFormat, "messages": messagesFormat, "gemini": geminiFormat}
	for name, f := range formats {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, 529} {
			w := httptest.NewRecorder()
			if got := f.writeError(w, &llm.Error{Code: llm.CodeUnavailable, Status: status, RetryAfterMS: 1500}); got != status {
				t.Fatalf("%s: status = %d, want %d", name, got, status)
			}
			if got := w.Header().Get("Retry-After"); got != "2" {
				t.Errorf("%s %d: Retry-After = %q, want 2", name, status, got)
			}
			w = httptest.NewRecorder()
			f.writeError(w, &llm.Error{Code: llm.CodeUnavailable, Status: status})
			if got := w.Header().Get("Retry-After"); got != "" {
				t.Errorf("%s %d with no wait: Retry-After = %q, want none", name, status, got)
			}
		}
		// Another status does not gain the header.
		w := httptest.NewRecorder()
		f.writeError(w, &llm.Error{Code: llm.CodeUpstream, Status: http.StatusBadGateway, RetryAfterMS: 1500})
		if got := w.Header().Get("Retry-After"); got != "" {
			t.Errorf("%s 502: Retry-After = %q, want none", name, got)
		}
	}
}

// A missing or wrong client key is final in every client format; an upstream's 401 is not this
// refusal and keeps its headers.
func TestClientKeyRefusalSaysDoNotRetry(t *testing.T) {
	h := NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}}}})
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/chat/completions"},
		{http.MethodPost, "/v1/responses"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodPost, "/v1beta/models/m:generateContent"},
		{http.MethodPost, "/v1/responses/compact"},
		{http.MethodGet, "/v1/models"},
		{http.MethodGet, "/v1beta/models"},
		{http.MethodGet, "/v1/responses"},
	} {
		for _, key := range []string{"", "wrong"} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
			if key != "" {
				r.Header.Set("Authorization", "Bearer "+key)
			}
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized || w.Header().Get("X-Should-Retry") != "false" {
				t.Errorf("%s %s key %q: status %d, X-Should-Retry %q; want 401 and false", c.method, c.path, key, w.Code, w.Header().Get("X-Should-Retry"))
			}
		}
	}
	w := httptest.NewRecorder()
	chatFormat.writeError(w, &llm.Error{Code: llm.CodeAuth, Status: http.StatusUnauthorized, ProviderCode: "invalid_api_key", Message: "missing or invalid API key"})
	if got := w.Header().Get("X-Should-Retry"); got != "" {
		t.Errorf("an upstream's 401: X-Should-Retry = %q, want none", got)
	}
}

type swapSource struct{ g atomic.Pointer[Generation] }

func (s *swapSource) Acquire() (*Generation, func()) { return s.g.Load(), func() {} }

// A key removed while a WebSocket is open is refused in an error message, whose headers say the
// same as the HTTP refusal's.
func TestClientKeyRefusalOnOpenWebSocket(t *testing.T) {
	src := &swapSource{}
	src.g.Store(&Generation{Config: &Config{ClientKeys: []string{"k"}}})
	srv := httptest.NewServer(NewHandler(src))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/responses", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer k"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	src.g.Store(&Generation{Config: &Config{ClientKeys: []string{"other"}}})
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Status != http.StatusUnauthorized || m.Headers["X-Should-Retry"] != "false" {
		t.Errorf("message = %s, want status 401 with X-Should-Retry false in headers", data)
	}
}
