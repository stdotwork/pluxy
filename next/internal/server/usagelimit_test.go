package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

func usageLimitError() *llm.Error {
	at := time.Now()
	u := &llm.UsageLimit{Model: "gpt-5", Provider: "codex", ResetsAt: at.Add(2 * time.Hour), WindowMinutes: 300, LastError: "usage_limit_reached: limit", At: at}
	return &llm.Error{Code: llm.CodeQuota, Status: 429, ProviderCode: "usage_limit_reached", Message: u.Message(),
		RetryAfterMS: (2 * time.Hour).Milliseconds(), UsageLimit: u}
}

// The usage-limit reply is a 429 without Retry-After, with X-Should-Retry false and Codex's
// window headers, and today's core's body: the same object for OpenAI, Responses and Gemini
// clients, a rate_limit_error with the time to the reset for Anthropic clients.
func TestUsageLimitReplies(t *testing.T) {
	for name, f := range map[string]clientFormat{"chat": chatFormat, "responses": responsesFormat, "gemini": geminiFormat, "messages": messagesFormat} {
		w := httptest.NewRecorder()
		if st := f.writeError(w, usageLimitError()); st != 429 || w.Code != 429 {
			t.Errorf("%s: status %d", name, w.Code)
		}
		h := w.Header()
		if h.Get("Retry-After") != "" || h.Get("X-Should-Retry") != "false" || h.Get("X-Codex-Primary-Window-Minutes") != "300" ||
			h.Get("X-Codex-Primary-Used-Percent") != "100" || h.Get("X-Codex-Primary-Reset-At") == "" {
			t.Errorf("%s: headers %v", name, h)
		}
		var b struct {
			Type  string
			Error map[string]any
		}
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "messages" {
			msg, _ := b.Error["message"].(string)
			if b.Type != "error" || b.Error["type"] != "rate_limit_error" || b.Error["retryable"] != false || b.Error["resets_at"] == nil ||
				!strings.HasPrefix(msg, "All credentials for model gpt-5 are cooling down via provider codex (last error: usage_limit_reached: limit) (reset after ") {
				t.Errorf("messages body = %s", w.Body)
			}
			continue
		}
		if b.Error["type"] != "usage_limit_reached" || b.Error["code"] != "model_cooldown" || b.Error["retryable"] != false || b.Error["limit_window_minutes"] != 300.0 {
			t.Errorf("%s body = %s", name, w.Body)
		}
	}
	// Without the mark, a 429 still tells the client when to retry.
	w := httptest.NewRecorder()
	chatFormat.writeError(w, &llm.Error{Code: llm.CodeQuota, Status: 429, RetryAfterMS: 1500})
	if w.Header().Get("Retry-After") != "2" || w.Header().Get("X-Should-Retry") != "" {
		t.Errorf("plain 429 headers = %v", w.Header())
	}
}

// A Chat Completions stream that ends with the usage-limit reply is closed with [DONE], as in
// today's core; any other error is not.
func TestUsageLimitEndsChatStreamWithDone(t *testing.T) {
	for _, e := range []*llm.Error{usageLimitError(), {Code: llm.CodeQuota, Status: 429, Message: "used up"}} {
		var buf bytes.Buffer
		sw := openaichat.NewStreamWriter(&buf, false)
		_ = sw.Write(llm.Event{Type: llm.EventResponseStart, Response: &llm.ResponseInfo{ID: "r", Model: "gpt-5"}})
		_ = sw.Write(llm.Event{Type: llm.EventError, Error: e})
		done := strings.HasSuffix(buf.String(), "data: [DONE]\n\n")
		if done != (e.UsageLimit != nil) {
			t.Errorf("usage limit %v: stream ends with [DONE] = %v:\n%s", e.UsageLimit != nil, done, buf.String())
		}
	}
}
