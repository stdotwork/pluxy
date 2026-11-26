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

// Multiple mid-conversation system runs separated only by user turns must all sit after the last
// user turn before the next assistant turn (or end of messages), merged into one system message,
// so no system message is followed by a user turn.
func TestEncodePlacesInterleavedMidConversationSystem(t *testing.T) {
	interleaved := []llm.Item{
		textItem(llm.RoleSystem, "S0"), textItem(llm.RoleUser, "u1"),
		textItem(llm.RoleSystem, "S1"), textItem(llm.RoleUser, "u2"),
		textItem(llm.RoleSystem, "S2"), textItem(llm.RoleUser, "u3"),
	}
	meetsAtEnd := []llm.Item{
		textItem(llm.RoleSystem, "S0"), textItem(llm.RoleUser, "u1"),
		textItem(llm.RoleSystem, "S1"), textItem(llm.RoleUser, "u2"),
		textItem(llm.RoleSystem, "S2"),
	}
	acrossAssistant := []llm.Item{
		textItem(llm.RoleSystem, "S0"), textItem(llm.RoleUser, "u1"),
		textItem(llm.RoleSystem, "S1"), textItem(llm.RoleUser, "u2"),
		textItem(llm.RoleSystem, "S2"), textItem(llm.RoleAssistant, "a1"),
		textItem(llm.RoleSystem, "S3"), textItem(llm.RoleUser, "u3"),
		textItem(llm.RoleSystem, "S4"), textItem(llm.RoleUser, "u4"),
	}
	for _, c := range []struct {
		name, model  string
		items        []llm.Item
		system, msgs string
		losses       []string
	}{
		{"interleaved system and user turns", "claude-opus-5-5", interleaved, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]},{"role":"user","content":[{"type":"text","text":"u3"}]},{"role":"system","content":[{"type":"text","text":"S1"},{"type":"text","text":"S2"}]}]`,
			[]string{"approximated items[2]", "approximated items[4]"}},
		{"moved run meets trailing system run", "claude-opus-5-5", meetsAtEnd, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]},{"role":"system","content":[{"type":"text","text":"S1"},{"type":"text","text":"S2"}]}]`,
			[]string{"approximated items[2]"}},
		{"interleaved runs on both sides of an assistant turn", "claude-opus-5-5", acrossAssistant, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]},{"role":"system","content":[{"type":"text","text":"S1"},{"type":"text","text":"S2"}]},{"role":"assistant","content":[{"type":"text","text":"a1"}]},{"role":"user","content":[{"type":"text","text":"u3"}]},{"role":"user","content":[{"type":"text","text":"u4"}]},{"role":"system","content":[{"type":"text","text":"S3"},{"type":"text","text":"S4"}]}]`,
			[]string{"approximated items[2]", "approximated items[6]", "approximated items[8]"}},
		{"older Claude keeps each reminder in its own user turn", "claude-sonnet-4-5", interleaved, `[{"type":"text","text":"S0"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"user","content":[{"type":"text","text":"u2"},{"type":"text","text":"<system-reminder>\nS1\n</system-reminder>"}]},{"role":"user","content":[{"type":"text","text":"u3"},{"type":"text","text":"<system-reminder>\nS2\n</system-reminder>"}]}]`,
			[]string{"approximated items[2]", "approximated items[4]"}},
		{"another vendor's model keeps all system in top-level prompt", "glm-4.6", interleaved, `[{"type":"text","text":"S0"},{"type":"text","text":"S1"},{"type":"text","text":"S2"}]`,
			`[{"role":"user","content":[{"type":"text","text":"u1"}]},{"role":"user","content":[{"type":"text","text":"u2"}]},{"role":"user","content":[{"type":"text","text":"u3"}]}]`, nil},
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
