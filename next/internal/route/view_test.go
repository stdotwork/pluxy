package route

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Runtime lists an account's live blocks on each scope, latest end first, with the error that
// set each (without its raw body) and the last failure; Reset clears them all, and the backoff
// step with them, and leaves other accounts' state alone.
func TestRuntimeAndReset(t *testing.T) {
	s := NewState()
	now := time.Unix(1_800_000_000, 0)
	a := Account{ID: "a"}
	other := Account{ID: "b"}
	s.failure(&a, "m1", 0, &llm.Error{Code: llm.CodeRateLimit, Scope: llm.ScopeCredential, Message: "slow down", RetryAfterMS: 60_000, Raw: json.RawMessage(`{"x":1}`)}, now, 0)
	s.failure(&a, "m2", 0, &llm.Error{Code: llm.CodeNotFound, Message: "no such model"}, now, 0)
	s.failure(&a, "m1", 0, &llm.Error{Code: llm.CodeUpstream, Message: "bad gateway", Status: 502}, now, 0)
	s.failure(&other, "m1", 0, &llm.Error{Code: llm.CodeQuota, Message: "used up", RetryAfterMS: 3_600_000}, now, 0)

	rt := s.Runtime(a, now.Add(100*time.Millisecond))
	if len(rt.Cooldowns) != 3 {
		t.Fatalf("cooldowns = %+v, want 3", rt.Cooldowns)
	}
	first := rt.Cooldowns[0]
	if first.Scope != "model" || first.Model != "m2" || first.Kind != "cooldown" || !first.Until.Equal(now.Add(notFoundCooldown)) {
		t.Errorf("latest block = %+v, want the 12h model block on m2", first)
	}
	var limit *Cooldown
	for i := range rt.Cooldowns {
		if rt.Cooldowns[i].Kind == "rate_limit" {
			limit = &rt.Cooldowns[i]
		}
	}
	if limit == nil || limit.Scope != "credential" || limit.Error == nil || limit.Error.Message != "slow down" || limit.Error.Raw != nil {
		t.Errorf("rate limit block = %+v, want credential scope with its error and no raw body", limit)
	}
	if !rt.CooldownUntil.Equal(now.Add(time.Minute)) {
		t.Errorf("cooldown until = %v, want the credential block's end %v (model blocks do not count)", rt.CooldownUntil, now.Add(time.Minute))
	}
	if rt.LastError == nil || rt.LastError.Message != "bad gateway" || !rt.LastErrorAt.Equal(now) {
		t.Errorf("last error = %+v at %v, want the bad gateway", rt.LastError, rt.LastErrorAt)
	}

	if n := s.Reset(a, now.Add(100*time.Millisecond)); n != 3 {
		t.Errorf("Reset cleared %d blocks, want 3", n)
	}
	if rt := s.Runtime(a, now.Add(time.Second)); len(rt.Cooldowns) != 0 || rt.LastError != nil || !rt.CooldownUntil.IsZero() {
		t.Errorf("after Reset: %+v", rt)
	}
	if until, _, _ := s.availability(&a, "m1", now.Add(time.Second)); !until.IsZero() {
		t.Errorf("after Reset the account is blocked until %v", until)
	}
	// The backoff step went with the block: the next transient failure starts from the bottom.
	s.rand = func() float64 { return 1 }
	s.failure(&a, "m1", 0, &llm.Error{Code: llm.CodeUpstream, Message: "again"}, now.Add(2*time.Second), 0)
	if rt := s.Runtime(a, now.Add(2*time.Second)); len(rt.Cooldowns) != 1 || rt.Cooldowns[0].BackoffLevel != 1 {
		t.Errorf("after a new failure: %+v, want one block at backoff level 1", rt.Cooldowns)
	}
	if rt := s.Runtime(other, now.Add(time.Second)); len(rt.Cooldowns) != 1 || rt.Cooldowns[0].Kind != "quota" {
		t.Errorf("the other account's state = %+v, want its quota block kept", rt.Cooldowns)
	}
}

// The last error goes with a success from an attempt that started after it, not with one that
// started before.
func TestLastErrorClearedBySuccess(t *testing.T) {
	s := NewState()
	now := time.Now()
	a := Account{ID: "a"}
	early, _ := s.reserve(&a, "m", now)
	newer, _ := s.reserve(&a, "m", now)
	s.failure(&a, "m", newer, &llm.Error{Code: llm.CodeRateLimit, Message: "slow down"}, now, 0)
	s.end(&a, newer)
	s.success(&a, "m", early)
	s.end(&a, early)
	if rt := s.Runtime(a, now); rt.LastError == nil {
		t.Fatal("an older success cleared the last error")
	}
	s.Reset(a, now)
	s.failure(&a, "m", 0, &llm.Error{Code: llm.CodeRateLimit, Message: "slow down"}, now, 0)
	late, _ := s.reserve(&a, "m", now.Add(time.Hour))
	s.success(&a, "m", late)
	s.end(&a, late)
	if rt := s.Runtime(a, now); rt.LastError != nil || len(rt.Cooldowns) != 0 {
		t.Fatalf("a later success left %+v", rt)
	}
	// A failure that is about the request records no last error.
	s.failure(&a, "m", 0, &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad request"}, now, 0)
	if rt := s.Runtime(a, now); rt.LastError != nil {
		t.Fatalf("a request failure was recorded: %+v", rt.LastError)
	}
}
