package anthropicmessages

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// RepairTurns sends Claude a conversation it accepts: no trailing assistant turn to a model that
// refuses prefill, every tool call answered in the next user turn and every tool result answering
// one, no empty user turn. Each repair is reported.
func TestRepairTurns(t *testing.T) {
	const (
		hi        = `{"role":"user","content":"Hi"}`
		prefill   = `{"role":"assistant","content":"Sure:"}`
		reply     = `{"role":"assistant","content":"ok"}`
		interrupt = `{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":"Tool call was interrupted before any output was recorded."}`
	)
	for _, c := range []struct {
		name, model, in, want string
		losses                []string
	}{
		// Prefill: refused from Claude 4.6 on, by Mythos Preview, and by a claude- name not known
		// as a version; older models and other names keep it.
		{"prefill to 4.6", "claude-opus-4-6", "[" + hi + "," + prefill + "]", "[" + hi + "]", []string{"dropped messages[1]"}},
		{"prefill to 5.5", "claude-sonnet-5-5", "[" + hi + "," + prefill + "]", "[" + hi + "]", []string{"dropped messages[1]"}},
		{"prefill to an unknown claude name", "claude-test", "[" + hi + "," + prefill + "]", "[" + hi + "]", []string{"dropped messages[1]"}},
		{"prefill to Mythos Preview", "claude-mythos-preview", "[" + hi + "," + prefill + "]", "[" + hi + "]", []string{"dropped messages[1]"}},
		{"prefill to Bedrock 4.7", "us.anthropic.claude-opus-4-7", "[" + hi + "," + prefill + "]", "[" + hi + "]", []string{"dropped messages[1]"}},
		{"two trailing assistant turns", "claude-opus-4-6", "[" + hi + "," + reply + "," + prefill + "]", "[" + hi + "]", []string{"dropped messages[1]", "dropped messages[2]"}},
		{"prefill to 4.5", "claude-sonnet-4-5", "[" + hi + "," + prefill + "]", "[" + hi + "," + prefill + "]", nil},
		{"prefill to Haiku 4.5", "claude-haiku-4-5-20251001", "[" + hi + "," + prefill + "]", "[" + hi + "," + prefill + "]", nil},
		{"prefill to 3.7", "claude-3-7-sonnet-20250219", "[" + hi + "," + prefill + "]", "[" + hi + "," + prefill + "]", nil},
		{"prefill to another vendor's model", "glm-4.6", "[" + hi + "," + prefill + "]", "[" + hi + "," + prefill + "]", nil},
		// A turn paused on a server tool is continued by sending it back last: not a prefill.
		{"paused server tool turn", "claude-opus-4-6",
			"[" + hi + `,{"role":"assistant","content":[{"type":"text","text":"Searching."},{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}}]}]`,
			"[" + hi + `,{"role":"assistant","content":[{"type":"text","text":"Searching."},{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"q"}}]}]`, nil},

		// Tool calls with no result get an error result, after the results the turn has.
		{"unanswered tool call", "claude-opus-4-6",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}},{"type":"tool_use","id":"t2","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"},{"type":"text","text":"and?"}]}]`,
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}},{"type":"tool_use","id":"t2","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"},` + interrupt + `,{"type":"text","text":"and?"}]}]`,
			[]string{"approximated messages[1].content[1]"}},
		{"unanswered tool call before a string user turn", "claude-opus-4-6",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"f","input":{}}]},{"role":"user","content":"go on"}]`,
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"f","input":{}}]},{"role":"user","content":[` + interrupt + `,{"type":"text","text":"go on"}]}]`,
			[]string{"approximated messages[1].content[0]"}},
		{"unanswered tool call before another assistant turn", "claude-sonnet-4-5",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"f","input":{}}]},` + reply + "]",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"f","input":{}}]},{"role":"user","content":[` + interrupt + "]}," + reply + "]",
			[]string{"approximated messages[1].content[0]"}},
		// The last turn of a model that takes prefill is left as it is, tool calls and all.
		{"tool call ending the conversation, prefill taken", "claude-sonnet-4-5",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]}]`,
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]}]`, nil},

		// A tool result that answers no call in the turn before becomes text in its user turn.
		{"orphaned tool result", "claude-opus-4-6",
			"[" + hi + "," + reply + `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"tx","content":"out"},{"type":"tool_result","tool_use_id":"ty","content":[{"type":"text","text":"more"}]},{"type":"tool_result","tool_use_id":"tz"},{"type":"text","text":"next"}]}]`,
			"[" + hi + "," + reply + `,{"role":"user","content":[{"type":"text","text":"out"},{"type":"text","text":"more"},{"type":"text","text":"(empty tool result)"},{"type":"text","text":"next"}]}]`,
			[]string{"approximated messages[2].content[0]", "approximated messages[2].content[1]", "approximated messages[2].content[2]"}},
		// Results go first in their turn, as Claude requires.
		{"text before a tool result", "claude-opus-4-6",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},{"role":"user","content":[{"type":"text","text":"x"},{"type":"tool_result","tool_use_id":"t1","content":"r1"}]}]`,
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"},{"type":"text","text":"x"}]}]`,
			[]string{"approximated messages[2].content[1]"}},

		// Empty user turns: blank text blocks are dropped; a turn left with nothing gets a
		// placeholder, which keeps the turns alternating and the conversation ending on the user.
		{"empty user turn", "claude-opus-4-6",
			"[" + hi + "," + reply + `,{"role":"user","content":""},` + reply + `,{"role":"user","content":[]}]`,
			"[" + hi + "," + reply + `,{"role":"user","content":[{"type":"text","text":"(empty message)"}]},` + reply + `,{"role":"user","content":[{"type":"text","text":"(empty message)"}]}]`,
			[]string{"approximated messages[2]", "approximated messages[4]"}},
		{"blank text blocks", "kimi-k2",
			`[{"role":"user","content":[{"type":"text","text":" \n"},{"type":"text","text":"Hi"}]},` + reply + `,{"role":"user","content":[{"type":"text","text":""}]}]`,
			`[{"role":"user","content":[{"type":"text","text":"Hi"}]},` + reply + `,{"role":"user","content":[{"type":"text","text":"(empty message)"}]}]`,
			[]string{"approximated messages[2]", "dropped messages[0].content[0]"}},

		// Nothing to repair: the body comes back as it was.
		{"well formed", "claude-opus-4-6",
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"}]}]`,
			"[" + hi + `,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"}]}]`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := `{"model":"m","max_tokens":10,"messages":` + c.in + "}"
			out, losses, err := RepairTurns([]byte(in), c.model)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "body", out, `{"model":"m","max_tokens":10,"messages":`+c.want+"}")
			if c.losses == nil && string(out) != in {
				t.Errorf("body changed with nothing to repair: %s", out)
			}
			want := slices.Clone(c.losses)
			slices.Sort(want)
			if got := lossList(losses); !slices.Equal(got, want) {
				t.Errorf("losses %v, want %v", got, want)
			}
		})
	}
}

// EncodeOptions.KeepModelTurn keeps a trailing assistant turn on a model that refuses prefill,
// for requests such as compaction that Anthropic takes ending on the model's turn; the other
// repairs still run (here the unanswered tool call gets its error result). Without the option
// the turn is still dropped.
func TestEncodeKeepModelTurn(t *testing.T) {
	text := func(role, s string) llm.Item {
		return llm.Item{Type: llm.ItemTypeMessage, Role: role, Content: []llm.Part{{Type: llm.PartText, Text: s}}}
	}
	req := llm.Request{Model: "claude-opus-4-6", Items: []llm.Item{
		text(llm.RoleUser, "List the files."),
		{Type: llm.ItemTypeToolCall, CallID: "toolu_1", Name: "shell", Arguments: "{}"},
		text(llm.RoleUser, "Go on."),
		text(llm.RoleAssistant, "There is one file, a.txt."),
	}}
	type sent struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	read := func(body []byte) sent {
		t.Helper()
		var s sent
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	body, losses, err := EncodeRequestWith(req, EncodeOptions{KeepModelTurn: true})
	if err != nil {
		t.Fatal(err)
	}
	kept := read(body)
	if n := len(kept.Messages); n != 4 || kept.Messages[3].Role != llm.RoleAssistant || kept.Messages[3].Content[0].Text != "There is one file, a.txt." {
		t.Errorf("kept: messages %s, want them to end on the assistant's reply", body)
	}
	if n := len(kept.Messages); n < 3 || kept.Messages[2].Content[0].Type != "tool_result" {
		t.Errorf("kept: messages %s, want the unanswered tool call answered in the next user turn", body)
	}
	if slices.ContainsFunc(losses, func(l llm.Loss) bool { return strings.Contains(l.Detail, "prefill") }) {
		t.Errorf("kept: losses %v, want no dropped turn", lossList(losses))
	}

	body, _, err = EncodeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if def := read(body); len(def.Messages) != 3 || def.Messages[2].Role != llm.RoleUser {
		t.Errorf("default: messages %s, want the trailing assistant turn dropped", body)
	}
}

// A conversation that is only a prefill has nothing left to send once it is dropped.
func TestRepairTurnsNothingLeft(t *testing.T) {
	_, _, err := RepairTurns([]byte(`{"messages":[{"role":"assistant","content":"Sure:"}]}`), "claude-opus-4-6")
	var e *llm.Error
	if !asError(err, &e) || e.Code != llm.CodeInvalidRequest {
		t.Errorf("err = %v, want an invalid_request error", err)
	}
}
