package main

import (
	"encoding/json"
	"testing"
)

// System messages in the conversation move into the top-level system prompt, after it, as text
// blocks: a string becomes a block, a text block keeps its cache marker, blank text and other
// blocks are left out. Without a system message that holds text the body is not touched.
func TestMoveSystemMessages(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"moved",
			`{"model":"m","system":"Be brief.","messages":[
				{"role":"user","content":"hi"},
				{"role":"system","content":"Now answer in French."},
				{"role":"assistant","content":"ok"},
				{"role":"System","content":[{"type":"text","text":"Cite sources.","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"url","url":"https://x"}},{"type":"text","text":"  "},"Be kind."]},
				{"role":"user","content":"go"}]}`,
			`{"model":"m","system":[{"type":"text","text":"Be brief."},{"type":"text","text":"Now answer in French."},{"type":"text","text":"Cite sources.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"Be kind."}],"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"ok"},
				{"role":"user","content":"go"}]}`},
		{"no system prompt", `{"messages":[{"role":"system","content":[{"type":"text","text":"Rules."}]},{"role":"user","content":"hi"}]}`,
			`{"system":[{"type":"text","text":"Rules."}],"messages":[{"role":"user","content":"hi"}]}`},
		{"no system message", `{"system":"S","messages":[{"role":"user","content":"hi"}]}`, ``},
		{"no text in it", `{"messages":[{"role":"system","content":" "},{"role":"user","content":"hi"}]}`, ``},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := moveSystemMessages([]byte(c.in))
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" {
				if string(got) != c.in {
					t.Errorf("body changed to %s", got)
				}
				return
			}
			if !jsonEqual(got, json.RawMessage(c.want)) {
				t.Errorf("got  %s\nwant %s", got, c.want)
			}
		})
	}
}
