package anthropicmessages

import (
	"testing"
)

// The version parser reads a Claude model name however a platform writes it: with a context
// window in brackets, a Vertex AI @version, a Bedrock prefix or in upper case. A minor version is
// never skipped except before an 8-digit date.
func TestParseClaudeVersion(t *testing.T) {
	for _, c := range []struct {
		model        string
		major, minor int
		ok           bool
	}{
		{"claude-opus-4-7[1m]", 4, 7, true},
		{"claude-opus-4-7@20260416", 4, 7, true},
		{"claude-opus-4-6[1m]", 4, 6, true},
		{"claude-opus-4@20250514", 4, 0, true},
		{"claude-sonnet-4@20250514", 4, 0, true},
		{"claude-3-7-sonnet@20250219", 3, 7, true},
		{"claude-3-5-haiku@20241022", 3, 5, true},
		{"claude-sonnet-4-20250514", 4, 0, true},
		{"claude-sonnet-4-5-20250929", 4, 5, true},
		{"claude-opus-4-1-20250805", 4, 1, true},
		{"claude-haiku-4-5-20251001", 4, 5, true},
		{"claude-3-7-sonnet-20250219", 3, 7, true},
		{"claude-sonnet-5-5-20261001", 5, 5, true},
		{"claude-opus-4-7", 4, 7, true},
		{"anthropic.claude-sonnet-4-5-20250929-v1:0", 4, 5, true},
		{"us.anthropic.claude-opus-4-7", 4, 7, true},
		{"global.anthropic.claude-fable-5-2", 5, 2, true},
		{"CLAUDE-OPUS-4-7", 4, 7, true},
		{"claude-mythos-5", 5, 0, true},
		{"claude-mythos-5-2", 5, 2, true},
		{"claude-5-fable", 5, 0, true},
		{"claude-opus-4-7x", 0, 0, false},
		{"claude-mythos-preview", 0, 0, false},
		{"claude-test", 0, 0, false},
		{"my-claude-model", 0, 0, false},
	} {
		major, minor, ok := parseClaudeVersion(c.model)
		if major != c.major || minor != c.minor || ok != c.ok {
			t.Errorf("parseClaudeVersion(%q) = %d, %d, %v; want %d, %d, %v", c.model, major, minor, ok, c.major, c.minor, c.ok)
		}
	}
}

// A null sampling setting is not a value: it does not count as set.
func TestDropRefusedSamplingNull(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","temperature":null,"top_p":0.9}`)
	got, losses, err := DropRefusedSampling(body, "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || len(losses) != 0 {
		t.Errorf("body %s, losses %+v; want the body unchanged (top_p alone)", got, losses)
	}
}
