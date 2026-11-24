package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/route"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// failingHandler returns the client API handler for accounts whose attempts fail as reply says,
// by account ID, and the public ID of each account.
func failingHandler(t *testing.T, cfg route.Config, accounts []route.Account, reply map[string]llm.Error) (http.Handler, func(id string) string) {
	t.Helper()
	attempt := func(_ context.Context, _ string, a protocol.Account, _ llm.Request) route.Stream {
		e := reply[a.ID]
		return &events{evs: []llm.Event{{Type: llm.EventError, Error: &e}}}
	}
	state := route.NewState()
	router, err := route.New(cfg, accounts, attempt, state)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(fixedSource{&Generation{Config: &Config{ClientKeys: []string{"k"}, Routing: cfg}, Router: router, Attempt: attempt}})
	return h, state.PublicID
}

// post sends a chat request for model and returns the reply's status and body.
func postChat(h http.Handler, model string) (int, string) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

// failureLines returns the log lines out holds that carry a failure for the request path.
func failureLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.Contains(l, "POST /v1/chat/completions") && strings.Contains(l, "[trace") && !strings.Contains(l, " | ") {
			lines = append(lines, l)
		}
	}
	return lines
}

var failureAccounts = func() []route.Account {
	return []route.Account{{ID: "a", Extension: "openai"}, {ID: "b", Extension: "openai"}, {ID: "c", Extension: "openai"}}
}

// A failed request logs one line that, after the message it always had, says the model asked for,
// each account tried with how it ended, and each account left out with why. It does so whether
// or not routing.explain shows the same to the client, and holds none of the upstream's words.
func TestFailureLineSaysWhatRoutingDid(t *testing.T) {
	for _, explain := range []bool{true, false} {
		t.Run(fmt.Sprintf("explain=%v", explain), func(t *testing.T) {
			out := captureLog(t)
			cfg := route.Config{Strategy: route.StrategyFillFirst, MaxAttempts: 2, Explain: &explain}
			reply := map[string]llm.Error{
				"a": {Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 600000, Message: "SECRET-UPSTREAM rate words"},
				"b": {Code: llm.CodeAuth, Status: 401, Scope: llm.ScopeCredential, Message: "SECRET-UPSTREAM auth words", Raw: []byte(`{"error":"SECRET-RAW"}`)},
			}
			h, pub := failingHandler(t, cfg, failureAccounts(), reply)
			code, body := postChat(h, "gpt-x")
			if code < 400 {
				t.Fatalf("status %d: %s", code, body)
			}
			if got := strings.Contains(body, `"pluxy"`); got != explain {
				t.Errorf("client body has the pluxy member = %v with routing.explain %v: %s", got, explain, body)
			}
			lines := failureLines(out.String())
			if len(lines) != 1 {
				t.Fatalf("log has %d failure lines, want 1:\n%s", len(lines), out)
			}
			line := lines[0]
			for _, want := range []string{
				" model=gpt-x",
				" tried=[" + pub("a") + "(openai): rate_limit; " + pub("b") + "(openai): auth]",
				" left_out=[" + pub("c") + ": skipped]",
			} {
				if !strings.Contains(line, want) {
					t.Errorf("log line does not contain %q:\n%s", want, line)
				}
			}
			// The prefix and message are what the line always had; the summary comes after them.
			if !strings.HasPrefix(line[strings.Index(line, "POST"):], "POST /v1/chat/completions") ||
				!strings.Contains(line, "]: auth: SECRET-UPSTREAM auth words model=gpt-x tried=[") {
				t.Errorf("log line does not keep its prefix and message before the summary:\n%s", line)
			}
			// The other account's message and the raw body are not in the log at all.
			if strings.Contains(out.String(), "rate words") || strings.Contains(out.String(), "SECRET-RAW") {
				t.Errorf("log holds upstream words the line did not carry before:\n%s", out)
			}
		})
	}
}

// An account cooling down is left out with the time its block ends.
func TestFailureLineSaysUntilWhen(t *testing.T) {
	out := captureLog(t)
	cfg := route.Config{Strategy: route.StrategyFillFirst}
	reply := map[string]llm.Error{"a": {Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 600000}}
	h, pub := failingHandler(t, cfg, []route.Account{{ID: "a", Extension: "openai"}}, reply)
	postChat(h, "gpt-x")
	out.Reset()
	postChat(h, "gpt-x") // a is cooling now: nothing is tried
	line := strings.Join(failureLines(out.String()), "\n")
	re := regexp.MustCompile(` model=gpt-x tried=\[\] left_out=\[` + pub("a") + `: cooling until (\S+)\]`)
	m := re.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("log line does not say the account is cooling and until when:\n%s", line)
	}
	until, err := time.Parse(time.RFC3339, m[1])
	if err != nil || time.Until(until) < 8*time.Minute || time.Until(until) > 11*time.Minute {
		t.Errorf("until = %q (%v), want about 10 minutes from now", m[1], err)
	}
}

// A long list is cut: the first eight accounts of each list, then a count of the rest.
func TestFailureLineCapsTheLists(t *testing.T) {
	out := captureLog(t)
	var accounts []route.Account
	reply := map[string]llm.Error{}
	for i := range 12 {
		id := fmt.Sprintf("acct%02d", i)
		accounts = append(accounts, route.Account{ID: id, Extension: "openai"})
		reply[id] = llm.Error{Code: llm.CodeUpstream, Status: 502, Retryable: true, Scope: llm.ScopeEndpoint}
	}
	cfg := route.Config{Strategy: route.StrategyFillFirst, MaxAttempts: 10, TransientErrorCooldownMS: -1}
	h, pub := failingHandler(t, cfg, accounts, reply)
	postChat(h, "gpt-x")
	lines := failureLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("log has %d failure lines, want 1:\n%s", len(lines), out)
	}
	line := lines[0]
	if !strings.Contains(line, pub("acct07")+"(openai): upstream; +2 more]") || strings.Contains(line, pub("acct08")+"(openai): upstream") {
		t.Errorf("tried list is not cut to 8 and counted:\n%s", line)
	}
	if !strings.Contains(line, " left_out=["+pub("acct10")+": skipped; "+pub("acct11")+": skipped]") {
		t.Errorf("left_out list is wrong:\n%s", line)
	}
}

// An error routing did not end, or one no model is asked for in, is logged as it always was.
func TestFailureLineWithoutRoutingIsUnchanged(t *testing.T) {
	out := captureLog(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	e := &llm.Error{Code: llm.CodeUpstream, Status: 502, Message: "stream broke"}
	logFailure(r, e)
	if got, want := out.String(), logPrefix(r)+": "+e.Error()+"\n"; !strings.HasSuffix(got, want) {
		t.Errorf("log line = %q, want it to end %q", got, want)
	}
}

// What a failure line holds of a model or account is one token: a model name with a space,
// a quote or a newline is quoted and cut, so it cannot pass for another field or another line.
func TestFailureLineQuotesWhatItDoesNotControl(t *testing.T) {
	out := captureLog(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	logFailure(r, &llm.Error{Code: llm.CodeUnavailable, Message: "m", Routed: &llm.Explain{Model: "x\n2026 fake line] tried=[" + strings.Repeat("y", 500), Tried: []llm.Tried{}, LeftOut: []llm.LeftOut{}}})
	if strings.Count(out.String(), "\n") != 1 {
		t.Errorf("a model name made the failure two lines:\n%q", out)
	}
	if !strings.Contains(out.String(), ` model="x\n2026 fake line] tried=[yyy`) {
		t.Errorf("a model name with spaces and a newline is not quoted:\n%q", out)
	}
	if strings.Contains(out.String(), strings.Repeat("y", 250)) {
		t.Errorf("a model name is not cut:\n%q", out)
	}
}
