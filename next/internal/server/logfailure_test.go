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

func postChat(h http.Handler, model string) (int, string) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer k")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

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
	return []route.Account{{ID: "alpha", Extension: "openai"}, {ID: "beta", Extension: "openai"}, {ID: "work laptop", Extension: "openai"}}
}

func TestFailureLineSaysWhatRoutingDid(t *testing.T) {
	for _, explain := range []bool{true, false} {
		t.Run(fmt.Sprintf("explain=%v", explain), func(t *testing.T) {
			out := captureLog(t)
			cfg := route.Config{Strategy: route.StrategyFillFirst, MaxAttempts: 2, Explain: &explain}
			reply := map[string]llm.Error{
				"alpha": {Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 600000, Message: "SECRET-UPSTREAM rate words"},
				"beta":  {Code: llm.CodeAuth, Status: 401, Scope: llm.ScopeCredential, Message: "SECRET-UPSTREAM auth words", Raw: []byte(`{"error":"SECRET-RAW"}`)},
			}
			h, pub := failingHandler(t, cfg, failureAccounts(), reply)
			code, body := postChat(h, "gpt-x")
			if code < 400 {
				t.Fatalf("status %d: %s", code, body)
			}
			if got := strings.Contains(body, `"pluxy"`); got != explain {
				t.Errorf("client body has the pluxy member = %v with routing.explain %v: %s", got, explain, body)
			}
			if explain {
				for _, id := range []string{"alpha", "beta"} {
					if !strings.Contains(body, `"account":"`+pub(id)+`"`) {
						t.Errorf("client body does not name account %s by its public ID %s: %s", id, pub(id), body)
					}
				}
			}
			for _, id := range []string{"alpha", "beta", "laptop"} {
				if strings.Contains(body, id) {
					t.Errorf("client body holds the config ID %q: %s", id, body)
				}
			}
			lines := failureLines(out.String())
			if len(lines) != 1 {
				t.Fatalf("log has %d failure lines, want 1:\n%s", len(lines), out)
			}
			line := lines[0]
			for _, want := range []string{
				" model=gpt-x",
				" tried=[alpha(openai): rate_limit; beta(openai): auth]",
				` left_out=["work laptop": skipped]`,
			} {
				if !strings.Contains(line, want) {
					t.Errorf("log line does not contain %q:\n%s", want, line)
				}
			}
			if !strings.HasPrefix(line[strings.Index(line, "POST"):], "POST /v1/chat/completions") ||
				!strings.Contains(line, "]: auth: SECRET-UPSTREAM auth words model=gpt-x tried=[") {
				t.Errorf("log line does not keep its prefix and message before the summary:\n%s", line)
			}
			if strings.Contains(out.String(), "rate words") || strings.Contains(out.String(), "SECRET-RAW") {
				t.Errorf("log holds upstream words the line did not carry before:\n%s", out)
			}
		})
	}
}

func TestFailureLineSaysUntilWhen(t *testing.T) {
	out := captureLog(t)
	cfg := route.Config{Strategy: route.StrategyFillFirst}
	reply := map[string]llm.Error{"alpha": {Code: llm.CodeRateLimit, Status: 429, Retryable: true, Scope: llm.ScopeCredential, RetryAfterMS: 600000}}
	h, _ := failingHandler(t, cfg, []route.Account{{ID: "alpha", Extension: "openai"}}, reply)
	postChat(h, "gpt-x")
	out.Reset()
	postChat(h, "gpt-x")
	line := strings.Join(failureLines(out.String()), "\n")
	re := regexp.MustCompile(` model=gpt-x tried=\[\] left_out=\[alpha: cooling until (\S+)\]`)
	m := re.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("log line does not say the account is cooling and until when:\n%s", line)
	}
	until, err := time.Parse(time.RFC3339, m[1])
	if err != nil || time.Until(until) < 8*time.Minute || time.Until(until) > 11*time.Minute {
		t.Errorf("until = %q (%v), want about 10 minutes from now", m[1], err)
	}
}

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
	h, _ := failingHandler(t, cfg, accounts, reply)
	postChat(h, "gpt-x")
	lines := failureLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("log has %d failure lines, want 1:\n%s", len(lines), out)
	}
	line := lines[0]
	if !strings.Contains(line, "acct07(openai): upstream; +2 more]") || strings.Contains(line, "acct08(openai): upstream") {
		t.Errorf("tried list is not cut to 8 and counted:\n%s", line)
	}
	if !strings.Contains(line, " left_out=[acct10: skipped; acct11: skipped]") {
		t.Errorf("left_out list is wrong:\n%s", line)
	}
}

func TestFailureLineWithoutRoutingIsUnchanged(t *testing.T) {
	out := captureLog(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	e := &llm.Error{Code: llm.CodeUpstream, Status: 502, Message: "stream broke"}
	logFailure(r, e)
	if got, want := out.String(), logPrefix(r)+": "+e.Error()+"\n"; !strings.HasSuffix(got, want) {
		t.Errorf("log line = %q, want it to end %q", got, want)
	}
}

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
