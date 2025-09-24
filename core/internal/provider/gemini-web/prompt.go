package geminiwebapi

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

var (
	reThink     = regexp.MustCompile(`(?s)^\s*<think>.*?</think>\s*`)
	reXMLAnyTag = regexp.MustCompile(`(?s)<\s*[^>]+>`)
)

// NormalizeRole converts a role to a standard format (lowercase, 'model' -> 'assistant').
func NormalizeRole(role string) string {
	r := strings.ToLower(role)
	if r == "model" {
		return "assistant"
	}
	return r
}

// NeedRoleTags checks if a list of messages requires role tags.
func NeedRoleTags(msgs []RoleText) bool {
	for _, m := range msgs {
		if strings.ToLower(m.Role) != "user" {
			return true
		}
	}
	return false
}

// AddRoleTag wraps content with a role tag.
func AddRoleTag(role, content string, unclose bool) string {
	if role == "" {
		role = "user"
	}
	if unclose {
		return "<|im_start|>" + role + "\n" + content
	}
	return "<|im_start|>" + role + "\n" + content + "\n<|im_end|>"
}

// BuildPrompt constructs the final prompt from a list of messages.
func BuildPrompt(msgs []RoleText, tagged bool, appendAssistant bool) string {
	if len(msgs) == 0 {
		if tagged && appendAssistant {
			return AddRoleTag("assistant", "", true)
		}
		return ""
	}
	if !tagged {
		var sb strings.Builder
		for i, m := range msgs {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(m.Text)
		}
		return sb.String()
	}
	var sb strings.Builder
	for _, m := range msgs {
		sb.WriteString(AddRoleTag(m.Role, m.Text, false))
		sb.WriteString("\n")
	}
	if appendAssistant {
		sb.WriteString(AddRoleTag("assistant", "", true))
	}
	return strings.TrimSpace(sb.String())
}

// RemoveThinkTags strips <think>...</think> blocks from a string.
func RemoveThinkTags(s string) string {
	return strings.TrimSpace(reThink.ReplaceAllString(s, ""))
}

// SanitizeAssistantMessages removes think tags from assistant messages.
func SanitizeAssistantMessages(msgs []RoleText) []RoleText {
	out := make([]RoleText, 0, len(msgs))
	for _, m := range msgs {
		if strings.ToLower(m.Role) == "assistant" {
			out = append(out, RoleText{Role: m.Role, Text: RemoveThinkTags(m.Text)})
		} else {
			out = append(out, m)
		}
	}
	return out
}

// AppendXMLWrapHintIfNeeded appends an XML wrap hint to messages containing XML-like blocks.
func AppendXMLWrapHintIfNeeded(msgs []RoleText, disable bool) []RoleText {
	if disable {
		return msgs
	}
	const xmlWrapHint = "\nFor any xml block, e.g. tool call, always wrap it with: \n`````xml\n...\n`````\n"
	out := make([]RoleText, 0, len(msgs))
	for _, m := range msgs {
		t := m.Text
		if reXMLAnyTag.MatchString(t) {
			t = t + xmlWrapHint
		}
		out = append(out, RoleText{Role: m.Role, Text: t})
	}
	return out
}

// EstimateTotalTokensFromRawJSON estimates token count by summing text parts.
func EstimateTotalTokensFromRawJSON(rawJSON []byte) int {
	totalChars := 0
	contents := gjson.GetBytes(rawJSON, "contents")
	if contents.Exists() {
		contents.ForEach(func(_, content gjson.Result) bool {
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if t := part.Get("text"); t.Exists() {
					totalChars += utf8.RuneCountInString(t.String())
				}
				return true
			})
			return true
		})
	}
	if totalChars <= 0 {
		return 0
	}
	return int(math.Ceil(float64(totalChars) / 4.0))
}
