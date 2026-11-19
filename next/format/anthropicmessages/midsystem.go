package anthropicmessages

import (
	"encoding/json"

	"github.com/stdotwork/pluxy/next/llm"
)

// roleSystem is the role of a system message inside a Messages conversation.
const roleSystem = "system"

// placeSystem places the system messages EncodeRequest kept inside the conversation, the
// consecutive ones as one, by Anthropic's rules for them
// (https://platform.claude.com/docs/en/build-with-claude/mid-conversation-system-messages): one
// follows a user turn and precedes an assistant turn or ends the conversation. So one is placed
// after the user turns that follow it, if any, as LiteLLM places them too. Where it then follows a
// user turn, a model that takes system messages there (takes) gets it as one; any other gets its
// text in that user turn, in place, as LiteLLM does, inside <system-reminder> tags (the form
// Claude Code gives such text) where LiteLLM adds an operator note. Where it follows an assistant
// turn with no user turn after it, its text goes in a user turn of its own. A move and a change of
// form are each reported as approximated.
func (e *encoder) placeSystem(msgs []*outMessage, takes bool) []*outMessage {
	out := make([]*outMessage, 0, len(msgs))
	for i := 0; i < len(msgs); {
		if msgs[i].role != roleSystem {
			out = append(out, msgs[i])
			i++
			continue
		}
		run := &outMessage{role: roleSystem, path: msgs[i].path}
		for i < len(msgs) && msgs[i].role == roleSystem {
			run.content = append(run.content, msgs[i].content...)
			run.paths = append(run.paths, msgs[i].paths...)
			i++
		}
		j := i
		for j < len(msgs) && msgs[j].role == llm.RoleUser {
			j++
		}
		moved := j > i
		out = append(out, msgs[i:j]...)
		i = j
		path := run.path
		if len(run.paths) > 0 {
			path = run.paths[0]
		}
		var user *outMessage
		if n := len(out); n > 0 && out[n-1].role == llm.RoleUser {
			user = out[n-1]
		}
		switch {
		case takes && user != nil:
			out = append(out, run)
			if moved {
				e.lose(llm.LossApproximated, path, "a system message may not come before a user turn; moved after the user turn that follows it")
			}
		case user != nil:
			user.content = append(user.content, reminders(run.content)...)
			user.paths = append(user.paths, run.paths...)
			e.lose(llm.LossApproximated, path, "this model takes no system message inside the conversation; sent in place as a <system-reminder> in the user turn")
		default:
			out = append(out, &outMessage{role: llm.RoleUser, content: reminders(run.content), paths: run.paths, path: path})
			e.lose(llm.LossApproximated, path, "a system message after an assistant turn with no user turn after it; sent as a <system-reminder> in a user turn of its own")
		}
	}
	return out
}

// reminders returns system content as user content: each text block's text inside
// <system-reminder> tags, its other fields kept.
func reminders(blocks []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(blocks))
	for _, b := range blocks {
		var m map[string]json.RawMessage
		var text string
		if json.Unmarshal(b, &m) != nil || blockType(b) != "text" || json.Unmarshal(m["text"], &text) != nil {
			out = append(out, b)
			continue
		}
		m["text"], _ = json.Marshal("<system-reminder>\n" + text + "\n</system-reminder>")
		raw, _ := json.Marshal(m)
		out = append(out, raw)
	}
	return out
}
