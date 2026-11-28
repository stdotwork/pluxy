package core

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxClientModel = 240

const codexTOMLNote = "If config.toml already has tables (lines such as [name]), put its first two lines, " +
	"model_provider and model, above the first table: a key written below a table line belongs to " +
	"that table, not to the file. The [model_providers.pluxy] block can go after them."

func checkClientModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if utf8.RuneCountInString(model) > maxClientModel || !utf8.ValidString(model) {
		return "", fmt.Errorf("model is at most %d characters of text", maxClientModel)
	}
	if strings.ContainsFunc(model, unicode.IsControl) {
		return "", fmt.Errorf("model has no control characters")
	}
	return model, nil
}

var notChat = []string{"embed", "image", "tts", "whisper", "moderation", "dall-e", "audio", "realtime", "transcribe"}

var clientModelPreferences = map[string][]string{
	"claude-code": {"claude-opus", "claude-sonnet", "claude", "opus", "sonnet"},
	"codex":       {"codex", "gpt-"},
}

func SuggestClientModel(tool string, names []string) string {
	if id, ok := clientSnippetID(tool); ok {
		tool = id
	}
	var chat []string
	for _, n := range names {
		if !containsAny(strings.ToLower(n), notChat) {
			chat = append(chat, n)
		}
	}
	for _, word := range clientModelPreferences[tool] {
		for _, n := range chat {
			if strings.Contains(strings.ToLower(n), word) {
				return n
			}
		}
	}
	if len(chat) > 0 {
		return chat[0]
	}
	return ""
}

func (c *Core) clientModel(ctx context.Context, tool, asked string) (string, []string) {
	s, release := c.acquire()
	served := s.Router.ServedModels(ctx)
	release()
	names := make([]string, len(served))
	for i, m := range served {
		names[i] = m.ID
	}
	if asked != "" {
		return asked, names
	}
	return SuggestClientModel(tool, names), names
}

func containsAny(s string, words []string) bool {
	for _, w := range words {
		if strings.Contains(s, w) {
			return true
		}
	}
	return false
}
