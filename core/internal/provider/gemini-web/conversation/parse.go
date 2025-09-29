package conversation

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ExtractMessages attempts to build a message list from the inbound request payload.
func ExtractMessages(handlerType string, raw []byte) []Message {
	if len(raw) == 0 {
		return nil
	}
	if msgs := extractOpenAIStyle(raw); len(msgs) > 0 {
		return msgs
	}
	if msgs := extractGeminiContents(raw); len(msgs) > 0 {
		return msgs
	}
	return nil
}

func extractOpenAIStyle(raw []byte) []Message {
	root := gjson.ParseBytes(raw)
	messages := root.Get("messages")
	if !messages.Exists() {
		return nil
	}
	out := make([]Message, 0, 8)
	messages.ForEach(func(_, entry gjson.Result) bool {
		role := strings.ToLower(strings.TrimSpace(entry.Get("role").String()))
		if role == "" {
			return true
		}
		if role == "system" {
			return true
		}
		// Ignore OpenAI tool messages to keep hashing aligned with
		// persistence (which only keeps text/inlineData for Gemini contents).
		// This avoids mismatches when a tool response is present: the
		// storage path drops tool payloads while the lookup path would
		// otherwise include them, causing sticky selection to fail.
		if role == "tool" {
			return true
		}
		var contentBuilder strings.Builder
		content := entry.Get("content")
		if !content.Exists() {
			out = append(out, Message{Role: role, Text: ""})
			return true
		}
		switch content.Type {
		case gjson.String:
			contentBuilder.WriteString(content.String())
		case gjson.JSON:
			if content.IsArray() {
				content.ForEach(func(_, part gjson.Result) bool {
					if text := part.Get("text"); text.Exists() {
						if contentBuilder.Len() > 0 {
							contentBuilder.WriteString("\n")
						}
						contentBuilder.WriteString(text.String())
					}
					return true
				})
			}
		}
		out = append(out, Message{Role: role, Text: contentBuilder.String()})
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractGeminiContents(raw []byte) []Message {
	contents := gjson.GetBytes(raw, "contents")
	if !contents.Exists() {
		return nil
	}
	out := make([]Message, 0, 8)
	contents.ForEach(func(_, entry gjson.Result) bool {
		role := strings.TrimSpace(entry.Get("role").String())
		if role == "" {
			role = "user"
		} else {
			role = strings.ToLower(role)
			if role == "model" {
				role = "assistant"
			}
		}
		var builder strings.Builder
		entry.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text"); text.Exists() {
				if builder.Len() > 0 {
					builder.WriteString("\n")
				}
				builder.WriteString(text.String())
			}
			return true
		})
		out = append(out, Message{Role: role, Text: builder.String()})
		return true
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
