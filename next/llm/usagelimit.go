package llm

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

// UsageLimit is what core's usage-limit reply says, in today's core's terms: every account
// ("credential") for Model is out of quota until ResetsAt, so the client should stop until then
// rather than retry.
type UsageLimit struct {
	Model string
	// Provider is the provider every account for the request belongs to; empty when they
	// belong to several.
	Provider string
	ResetsAt time.Time
	// WindowMinutes is the length of the usage window that ran out; 0 when not known.
	WindowMinutes int64
	// LastError summarises the last upstream failure, as "code: message".
	LastError string
	// At is when the limit was found: the reset's countdown starts there.
	At time.Time
}

// codexSecondaryWindowMinutes: a usage window this long or longer is Codex's secondary
// (weekly) window; a shorter one its primary (five-hour) window.
const codexSecondaryWindowMinutes = 24 * 60

// Message is the reply's message, as today's core words it.
func (u *UsageLimit) Message() string {
	model := u.Model
	if model == "" {
		model = "requested model"
	}
	msg := fmt.Sprintf("All credentials for model %s are cooling down", model)
	if u.Provider != "" {
		msg += " via provider " + u.Provider
	}
	if u.LastError != "" {
		msg += fmt.Sprintf(" (last error: %s)", u.LastError)
	}
	return msg
}

// resetIn is how long until the reset, from At.
func (u *UsageLimit) resetIn() time.Duration { return max(u.ResetsAt.Sub(u.At), 0) }

// ErrorObject is the error object of the reply's JSON body ({"error": ...}) for OpenAI, Responses
// and Gemini clients, with today's core's fields: code model_cooldown, type usage_limit_reached,
// the reset as Unix seconds (resets_at) and as a countdown (reset_seconds, reset_time),
// retryable false, and the window, provider and last upstream error when known.
func (u *UsageLimit) ErrorObject() map[string]any {
	in := u.resetIn()
	shown := in.Round(time.Second)
	if in > 0 && in < time.Second {
		shown = time.Second
	}
	o := map[string]any{
		"code":          "model_cooldown",
		"message":       u.Message(),
		"model":         u.Model,
		"reset_time":    shown.String(),
		"reset_seconds": int64(math.Ceil(in.Seconds())),
		"type":          "usage_limit_reached",
		"resets_at":     u.ResetsAt.Unix(),
		"retryable":     false,
	}
	if u.WindowMinutes > 0 {
		o["limit_window_minutes"] = u.WindowMinutes
	}
	if u.Provider != "" {
		o["provider"] = u.Provider
	}
	if u.LastError != "" {
		o["last_upstream_error"] = u.LastError
	}
	return o
}

// ResetAfter is the countdown in the message Anthropic clients get: whole seconds, at least one.
func (u *UsageLimit) ResetAfter() time.Duration {
	return max(u.resetIn().Round(time.Second), time.Second)
}

// Headers are the reply's headers, in place of Retry-After (a client that honours Retry-After
// would sleep through the whole window instead of stopping): X-Should-Retry false, which
// OpenAI's and Anthropic's SDKs honour ahead of the status, and, when the window is known, the
// rate-limit headers Codex reads with a usage limit (the primary slot for a window shorter
// than a day, the secondary for a longer one).
func (u *UsageLimit) Headers() [][2]string {
	h := [][2]string{{"X-Should-Retry", "false"}}
	if u.WindowMinutes > 0 {
		prefix := "X-Codex-Primary-"
		if u.WindowMinutes >= codexSecondaryWindowMinutes {
			prefix = "X-Codex-Secondary-"
		}
		h = append(h, [2]string{prefix + "Used-Percent", "100"},
			[2]string{prefix + "Window-Minutes", strconv.FormatInt(u.WindowMinutes, 10)},
			[2]string{prefix + "Reset-At", strconv.FormatInt(u.ResetsAt.Unix(), 10)})
	}
	return h
}
