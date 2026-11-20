package anthropicmessages

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

func textItem(role, text string) llm.Item {
	return llm.Item{Type: llm.ItemTypeMessage, Role: role, Content: []llm.Part{{Type: llm.PartText, Text: text}}}
}

// System text after the conversation has started stays where it was, so the prefix Claude caches
// does not change: as a system message on a model that takes one there, after the user turn it
// follows (or the next one, since it may not follow an assistant turn); on other Claude models
// as a <system-reminder> in that user turn. Leading system text is the system prompt, and other
// vendors' models get every system message there, as before.
func TestEncodeKeepsMidConversationSystemInPlace(t *testing.T) {
	const reminder = "<system-reminder>\\nS1\\n</system-reminder>"
	afterAssistant := []llm.Item{textItem(llm.RoleSystem, "S0"), textItem(llm.RoleUser, "u1"), textItem(llm.RoleAssistant, "a1"),
		textItem(llm.RoleDeveloper, "S1"), textItem(llm.RoleUser, "u2")}
	afterUser := []llm.Item{textItem(llm.RoleSystem, "S0"), textItem(llm.RoleUser, "u1"), textItem(llm.RoleSystem, "S1")}
	unanswered := []llm.Item{textItem(llm.RoleSystem, "S0"), textItem(llm.RoleUser, "u1"), textItem(llm.RoleAssistant, "a1"), textItem(llm.RoleSystem, "S1")}
	for _, c := range []struct {
		name, model  string
		items        []llm.Item
		system, msgs string
		losses       []string
	}{
		{"takes system messages, after an assistant turn", "claude-opus-5-5", afterAssistant, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]},{"role":"system","content":[{"type":"text","text":"S1"}]}]`,
			[]string{"approximated items[3]"}},
		{"takes system messages, after a user turn", "claude-fable-5-2", afterUser, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"system","content":[{"type":"text","text":"S1"}]}]`, nil},
		{"takes system messages, no user turn after the assistant's", "claude-opus-4-8", unanswered, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"` + reminder + `"}]}]`,
			[]string{"approximated items[3]"}},
		{"older Claude", "claude-sonnet-4-5", afterAssistant, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u2"},{"type":"text","text":"` + reminder + `"}]}]`,
			[]string{"approximated items[3]"}},
		{"Sonnet 5 takes none", "claude-sonnet-5", afterUser, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"},{"type":"text","text":"` + reminder + `"}]}]`,
			[]string{"approximated items[2]"}},
		{"another vendor's model", "glm-4.6", afterAssistant, `[{"type":"text","text":"S0"},{"type":"text","text":"S1"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]}]`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			body, losses, err := EncodeRequest(llm.Request{Model: c.model, Items: c.items})
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "system", fieldOf(t, body, "system"), c.system)
			sameJSON(t, "messages", fieldOf(t, body, "messages"), c.msgs)
			want := slices.Clone(c.losses)
			slices.Sort(want)
			if got := lossList(losses); !slices.Equal(got, want) {
				t.Errorf("losses %v, want %v", got, want)
			}
		})
	}
}

// fieldOf returns one top-level field of a JSON body.
func fieldOf(t *testing.T, body []byte, key string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m[key]
}
