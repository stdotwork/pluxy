package route

import (
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// wantExhausted fails the test unless e is marked exhausted at now with the soonest account back
// in wait (0: none comes back until a person acts), and WaitCannotHelp says stop.
func wantExhausted(t *testing.T, e *llm.Error, now time.Time, wait time.Duration, stop bool) {
	t.Helper()
	x := e.Exhausted
	if x == nil {
		t.Fatalf("error %q is not marked exhausted", e.Message)
	}
	want := time.Time{}
	if wait > 0 {
		want = now.Add(wait)
	}
	if !x.At.Equal(now) || !x.Until.Equal(want) {
		t.Errorf("exhausted = %+v, want at %v until %v", *x, now, want)
	}
	if got := WaitCannotHelp(e); got != stop {
		t.Errorf("WaitCannotHelp = %v, want %v", got, stop)
	}
}

// When no account can take the request, its error says when the soonest one can: on the request
// whose attempts set the blocks and on the next one, which finds them. A wait longer than
// usageLimitMinReset cannot help a client; a shorter one can.
func TestExhaustedNamesTheSoonestAccount(t *testing.T) {
	h := newHarness(t, Config{}, accts("a", "b")...)
	h.on("a", rateLimited(6831*time.Second))
	h.on("b", rateLimited(2*time.Hour))
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 6831*time.Second, true)
	e := lastError(t, h.run(h.req("m")))
	if e.ProviderCode != "model_cooldown" {
		t.Fatalf("second request: %+v, want core's cooling reply", e)
	}
	wantExhausted(t, e, h.now, 6831*time.Second, true)
	h.wantAttempts("a", "b")

	h = newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(30*time.Second))
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 30*time.Second, false)

	// Exactly the threshold is still worth waiting for.
	h = newHarness(t, Config{}, accts("a")...)
	h.on("a", rateLimited(usageLimitMinReset))
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, usageLimitMinReset, false)
}

// An account that could take the request now leaves its error unmarked: the request failed for
// another reason, and a client may try again as it does today.
func TestNotExhaustedWhileAnAccountCanServe(t *testing.T) {
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, Account{ID: "a", Priority: 1}, Account{ID: "b"})
	h.on("a", rateLimited(2*time.Hour))
	h.on("b", fail(llm.Error{Code: llm.CodeInvalidRequest, Status: 400, Scope: llm.ScopeRequest, Message: "bad request"}))
	if e := lastError(t, h.run(h.req("m"))); e.Exhausted != nil || WaitCannotHelp(e) {
		t.Errorf("a refused request: exhausted %+v", e.Exhausted)
	}

	// The account at its concurrency limit frees up when a reply ends.
	h = newHarness(t, Config{}, Account{ID: "a", MaxConcurrency: 1})
	s := h.r.Generate(t.Context(), h.req("m"))
	s.Next()
	if e := lastError(t, h.run(h.req("m"))); e.ProviderCode != "accounts_busy" || e.Exhausted != nil {
		t.Errorf("a busy account: %+v, exhausted %+v", e, e.Exhausted)
	}
	s.Close()
}

// Accounts turned off, waiting for a new sign-in or closed by their provider never come back on
// their own: with nothing else, the error says none will, and a client should stop. With another
// account cooling, its end is the soonest.
func TestExhaustedWithNoAccountComingBack(t *testing.T) {
	h := newHarness(t, Config{}, Account{ID: "h", Held: HealthNeedsLogin}, Account{ID: "off", Held: HealthDisabled})
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 0, true)

	h = newHarness(t, Config{}, accts("a")...)
	h.on("a", fail(llm.Error{Code: llm.CodeAccountClosed, Status: 403, Retryable: true, Scope: llm.ScopeCredential}))
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 0, true)
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 0, true)

	h = newHarness(t, Config{}, Account{ID: "h", Held: HealthNeedsLogin}, Account{ID: "a"})
	h.on("a", rateLimited(2*time.Minute))
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 2*time.Minute, false)
}

// An extension that does not run serves none of its accounts, tried or not; one restarting after
// a crash serves them again when it says.
func TestExhaustedWhenTheProviderDoesNotRun(t *testing.T) {
	down := fail(llm.Error{Code: llm.CodeExtensionUnavailable, Message: "extension x does not run"})
	h := newHarness(t, Config{Strategy: StrategyFillFirst}, Account{ID: "a", Extension: "x", Priority: 1}, Account{ID: "b", Extension: "x"})
	h.on("a", down)
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 0, true)
	h.wantAttempts("a")

	h = newHarness(t, Config{}, Account{ID: "a", Extension: "x"})
	h.on("a", fail(llm.Error{Code: llm.CodeExtensionUnavailable, Message: "extension x crashed; restarting in 2s", RetryAfterMS: 2000}))
	wantExhausted(t, lastError(t, h.run(h.req("m"))), h.now, 2*time.Second, false)
}
