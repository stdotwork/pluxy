package core

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a client tool's setup text says about its model: the tool asks for a model of its own
// choosing unless told which one, and an account of another service serves none of those, so the
// text names one pluxy serves.

// maxClientModel is the longest model name the setup text takes (the longest the desktop app's
// writers take).
const maxClientModel = 240

// codexTOMLNote is what a Codex config.toml that already has tables needs: TOML puts a key
// written below a [table] line inside that table.
const codexTOMLNote = "If config.toml already has tables (lines such as [name]), put its first two lines, " +
	"model_provider and model, above the first table: a key written below a table line belongs to " +
	"that table, not to the file. The [model_providers.pluxy] block can go after them."

// checkClientModel trims a model name for the setup text and refuses one the text cannot hold
// safely: too long, or with a control character (the name is written into JSON, TOML and YAML).
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

// notChat are the words of models that are no coding tool's model: they are not the best match
// for one, and a tool's setup text names one only when nothing else is served.
var notChat = []string{"embed", "image", "tts", "whisper", "moderation", "dall-e", "audio", "realtime", "transcribe"}

// clientModelPreferences are, for each tool, the words that mark the model it was made for, best
// first: the first model whose name holds the first word that any name holds is the best match.
var clientModelPreferences = map[string][]string{
	"claude-code": {"claude-opus", "claude-sonnet", "claude", "opus", "sonnet"},
	"codex":       {"codex", "gpt-"},
}

// SuggestClientModel is the model among names (the models pluxy serves, in the order it lists
// them) that a tool should start with: the one made for that tool when pluxy serves one, else the
// first that is a chat model. It is empty when names holds none.
func SuggestClientModel(tool string, names []string) string {
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

// clientModel returns the model tool should start with and the names of the models core serves:
// asked when non-empty, else the best match among the served models (empty when none is served).
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
