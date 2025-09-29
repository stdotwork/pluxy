package geminiwebapi

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	conversation "github.com/router-for-me/CLIProxyAPI/v6/internal/provider/gemini-web/conversation"
	"github.com/tidwall/gjson"
)

var (
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
	return conversation.RemoveThinkTags(s)
}

// SanitizeAssistantMessages removes think tags from assistant messages.
func SanitizeAssistantMessages(msgs []RoleText) []RoleText {
	cleaned := conversation.SanitizeAssistantMessages(msgs)
	return cleaned
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

// Request chunking helpers ------------------------------------------------

const continuationHint = "\n(More messages to come, please reply with just 'ok.')"

func ChunkByRunes(s string, size int) []string {
	if size <= 0 {
		return []string{s}
	}
	chunks := make([]string, 0, (len(s)/size)+1)
	var buf strings.Builder
	count := 0
	for _, r := range s {
		buf.WriteRune(r)
		count++
		if count >= size {
			chunks = append(chunks, buf.String())
			buf.Reset()
			count = 0
		}
	}
	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

func MaxCharsPerRequest(cfg *config.Config) int {
	// Read max characters per request from config with a conservative default.
	if cfg != nil {
		if v := cfg.GeminiWeb.MaxCharsPerRequest; v > 0 {
			return v
		}
	}
	return 1_000_000
}

func SendWithSplit(chat *ChatSession, text string, files []string, cfg *config.Config) (ModelOutput, error) {
	// Validate chat session
	if chat == nil {
		return ModelOutput{}, fmt.Errorf("nil chat session")
	}

	// Resolve maxChars characters per request
	maxChars := MaxCharsPerRequest(cfg)
	if maxChars <= 0 {
		maxChars = 1_000_000
	}

	// If within limit, send directly
	if utf8.RuneCountInString(text) <= maxChars {
		return chat.SendMessage(text, files)
	}

	// Decide whether to use continuation hint (enabled by default)
	useHint := true
	if cfg != nil && cfg.GeminiWeb.DisableContinuationHint {
		useHint = false
	}

	// Compute chunk size in runes. If the hint does not fit, disable it for this request.
	hintLen := 0
	if useHint {
		hintLen = utf8.RuneCountInString(continuationHint)
	}
	chunkSize := maxChars - hintLen
	if chunkSize <= 0 {
		// maxChars is too small to accommodate the hint; fall back to no-hint splitting
		useHint = false
		chunkSize = maxChars
	}

	// Split into rune-safe chunks
	chunks := ChunkByRunes(text, chunkSize)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	// Send all but the last chunk without files, optionally appending hint
	for i := 0; i < len(chunks)-1; i++ {
		part := chunks[i]
		if useHint {
			part += continuationHint
		}
		if _, err := chat.SendMessage(part, nil); err != nil {
			return ModelOutput{}, err
		}
	}

	// Send final chunk with files and return the actual output
	return chat.SendMessage(chunks[len(chunks)-1], files)
}
