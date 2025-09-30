package conversation

import (
	"regexp"
	"strings"
)

var reThink = regexp.MustCompile(`(?is)<think>.*?</think>`)

// RemoveThinkTags strips <think>...</think> blocks and trims whitespace.
func RemoveThinkTags(s string) string {
	return strings.TrimSpace(reThink.ReplaceAllString(s, ""))
}

// SanitizeAssistantMessages removes think tags from assistant messages while leaving others untouched.
func SanitizeAssistantMessages(msgs []Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if strings.EqualFold(strings.TrimSpace(m.Role), "assistant") {
			out = append(out, Message{Role: m.Role, Text: RemoveThinkTags(m.Text)})
			continue
		}
		out = append(out, m)
	}
	return out
}

// EqualMessages compares two message slices for equality.
func EqualMessages(a, b []Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}
