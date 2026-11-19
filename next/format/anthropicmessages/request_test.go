package anthropicmessages

import (
	"slices"
	"testing"
)

func TestIsClaudeCode(t *testing.T) {
	const ua = "claude-cli/2.1.280 (external, cli)"
	const beta = "interleaved-thinking-2025-05-14, claude-code-20250219"
	for _, c := range []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"cli", map[string]string{"x-app": "cli", "user-agent": ua, "anthropic-beta": beta}, true},
		{"cli-bg", map[string]string{"x-app": "cli-bg", "user-agent": ua, "anthropic-beta": beta}, true},
		{"other x-app", map[string]string{"x-app": "web", "user-agent": ua, "anthropic-beta": beta}, false},
		{"missing x-app", map[string]string{"user-agent": ua, "anthropic-beta": beta}, false},
		{"non-cli user-agent", map[string]string{"x-app": "cli-bg", "user-agent": "other/1.0", "anthropic-beta": beta}, false},
		{"missing claude-code beta", map[string]string{"x-app": "cli-bg", "user-agent": ua, "anthropic-beta": "interleaved-thinking-2025-05-14"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := IsClaudeCode(c.headers); got != c.want {
				t.Errorf("IsClaudeCode(%v) = %v, want %v", c.headers, got, c.want)
			}
		})
	}
}

func TestClaudeCodeHeadersIncludesPromptID(t *testing.T) {
	if !slices.Contains(ClaudeCodeHeaders, "x-claude-code-prompt-id") {
		t.Errorf("ClaudeCodeHeaders = %q, want x-claude-code-prompt-id included", ClaudeCodeHeaders)
	}
}
