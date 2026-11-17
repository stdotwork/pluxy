package llm

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// The usage-limit reply's body, message and headers are today's core's.
func TestUsageLimitReply(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	u := &UsageLimit{Model: "gpt-5", Provider: "codex", ResetsAt: at.Add(2*time.Hour - 300*time.Millisecond), WindowMinutes: 300,
		LastError: "usage_limit_reached: The usage limit has been reached", At: at}
	b, _ := json.Marshal(u.ErrorObject())
	want := `{"code":"model_cooldown","last_upstream_error":"usage_limit_reached: The usage limit has been reached","limit_window_minutes":300,` +
		`"message":"All credentials for model gpt-5 are cooling down via provider codex (last error: usage_limit_reached: The usage limit has been reached)",` +
		`"model":"gpt-5","provider":"codex","reset_seconds":7200,"reset_time":"2h0m0s","resets_at":1800007199,"retryable":false,"type":"usage_limit_reached"}`
	if string(b) != want {
		t.Errorf("body =\n%s\nwant\n%s", b, want)
	}
	if h := u.Headers(); !slices.Equal(h, [][2]string{{"X-Should-Retry", "false"}, {"X-Codex-Primary-Used-Percent", "100"},
		{"X-Codex-Primary-Window-Minutes", "300"}, {"X-Codex-Primary-Reset-At", "1800007199"}}) {
		t.Errorf("headers = %v", h)
	}
	u.WindowMinutes = 7 * 24 * 60
	if h := u.Headers(); len(h) != 4 || h[1][0] != "X-Codex-Secondary-Used-Percent" {
		t.Errorf("a weekly window's headers = %v, want the secondary slot", h)
	}

	bare := &UsageLimit{ResetsAt: at.Add(400 * time.Millisecond), At: at}
	o := bare.ErrorObject()
	for _, k := range []string{"provider", "limit_window_minutes", "last_upstream_error"} {
		if _, ok := o[k]; ok {
			t.Errorf("%s is set without a value: %v", k, o)
		}
	}
	if o["message"] != "All credentials for model requested model are cooling down" || o["reset_time"] != "1s" || o["reset_seconds"] != int64(1) {
		t.Errorf("bare reply = %v", o)
	}
	if h := bare.Headers(); len(h) != 1 {
		t.Errorf("headers without a window = %v, want only X-Should-Retry", h)
	}
	if d := bare.ResetAfter(); d != time.Second {
		t.Errorf("ResetAfter = %v, want at least a second", d)
	}
}

// The countdown in the reply is whole seconds rounded up, the same in reset_time, reset_seconds and
// the Anthropic message: 1.2s left is 2s in all three.
func TestUsageLimitCountdownRoundsUp(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	u := &UsageLimit{ResetsAt: at.Add(1200 * time.Millisecond), At: at}
	o := u.ErrorObject()
	if o["reset_time"] != "2s" || o["reset_seconds"] != int64(2) || u.ResetAfter() != 2*time.Second {
		t.Errorf("1.2s left: reset_time %v, reset_seconds %v, ResetAfter %v; want 2s, 2 and 2s", o["reset_time"], o["reset_seconds"], u.ResetAfter())
	}
}
