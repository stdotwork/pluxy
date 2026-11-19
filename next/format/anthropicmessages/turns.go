package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Claude answers some conversations with a 400 that a client cannot always avoid: a tool call left
// without a result (a session that stopped while a tool ran), a tool result with no call before it,
// an empty user turn, and on newer models a conversation that ends on an assistant turn. The
// repairs here send such a conversation in a form Claude takes, and report each change.
//
// The tool pairing follows the old core's repairClaudeToolPairing (CLIProxyAPI,
// internal/translator/claude/openai/responses/claude_openai-responses_request.go): a missing
// result is sent as an error result, and a result that answers no call is sent as text.

const (
	// interruptedResult is the error result sent for a tool call that has none, as the old core
	// words it.
	interruptedResult = "Tool call was interrupted before any output was recorded."
	// emptyToolResult stands in for a tool result with nothing in it once it is sent as text.
	emptyToolResult = "(empty tool result)"
	// emptyMessage stands in for a user turn with nothing in it, which Claude refuses.
	emptyMessage = "(empty message)"
)

// refusesPrefill reports whether model refuses a conversation that ends on an assistant turn:
// Claude 4.6 and later and Mythos Preview do ("Claude 4.6 and later models and Claude Mythos
// Preview reject a prefilled final assistant turn with a 400 error", Anthropic's extended thinking
// page), and so is a claude- name not known as a version, as every model since 4.6 refuses it.
// Older Claude models and other vendors' models take one.
func refusesPrefill(model string) bool {
	if !strings.HasPrefix(claudeName(model), "claude-") {
		return false
	}
	major, minor, ok := parseClaudeVersion(model)
	return !ok || major > 4 || (major == 4 && minor >= 6)
}

// claudeFamily reads the model line of a Claude model name in the newer form.
var claudeFamily = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-`)

// takesMidSystem reports whether model takes a system message inside the conversation: Claude
// Opus 4.8 and later, Fable and Mythos 5 and later, Sonnet and Haiku 5.5 and later (not Sonnet 5),
// as Anthropic's prompt caching page lists them. Any other name, a Claude one not placed as a
// version included, is treated as one that does not.
//
// shortcut: decided from the model name, as claudeVersion is.
func takesMidSystem(model string) bool {
	f := claudeFamily.FindStringSubmatch(claudeName(model))
	if f == nil {
		return false
	}
	major, minor, ok := parseClaudeVersion(model)
	if !ok {
		return false
	}
	from := func(ma, mi int) bool { return major > ma || (major == ma && minor >= mi) }
	switch f[1] {
	case "opus":
		return from(4, 8)
	case "fable", "mythos":
		return from(5, 0)
	default:
		return from(5, 5)
	}
}

// RepairTurns repairs the conversation of a Messages body for model, a Messages client's own
// body, as EncodeRequest repairs a translated one:
//   - a tool call the next user turn does not answer gets an error result there (a new user turn
//     when an assistant turn follows), after the results the turn has; a tool result that answers
//     no call in the assistant turn just before it is sent as text in its place; the answered
//     results go first in their user turn, as Claude requires. A user turn that needs any of this
//     is sent as one message, the consecutive user messages it was made of put together, as
//     Claude reads them anyway;
//   - blank text blocks in a user turn are dropped, and a user turn left with nothing gets a
//     placeholder text;
//   - for a model that refuses prefill (refusesPrefill), trailing assistant turns are dropped,
//     except a last turn paused on a server tool (its last block a server_tool_use), which is how
//     a paused turn is continued.
//
// Each change is reported, with its path in body. A body that needs none is returned as it came,
// and so is one whose messages it cannot read. A conversation with nothing left once its
// trailing assistant turns are dropped is an invalid_request error.
func RepairTurns(body []byte, model string) ([]byte, []llm.Loss, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, nil, err
	}
	var raw []json.RawMessage
	if json.Unmarshal(m["messages"], &raw) != nil {
		return body, nil, nil
	}
	msgs := make([]*outMessage, 0, len(raw))
	for i, r := range raw {
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(r, &msg) != nil {
			return body, nil, nil
		}
		om := &outMessage{role: msg.Role, raw: r, path: fmt.Sprintf("messages[%d]", i)}
		var s string
		if json.Unmarshal(msg.Content, &s) == nil {
			if s != "" {
				om.content = []json.RawMessage{textBlock(s)}
				om.paths = []string{om.path + ".content"}
			}
		} else if json.Unmarshal(msg.Content, &om.content) == nil {
			for j := range om.content {
				om.paths = append(om.paths, fmt.Sprintf("%s.content[%d]", om.path, j))
			}
		}
		msgs = append(msgs, om)
	}
	e := &encoder{}
	out, err := e.repairTurns(msgs, model)
	if err != nil {
		return nil, nil, err
	}
	if len(e.losses) == 0 {
		return body, nil, nil
	}
	list := make([]json.RawMessage, len(out))
	for i, om := range out {
		if om.raw != nil && !om.changed {
			list[i] = om.raw
			continue
		}
		fields := map[string]json.RawMessage{}
		_ = json.Unmarshal(om.raw, &fields)
		fields["role"], _ = json.Marshal(om.role)
		fields["content"], _ = json.Marshal(om.content)
		list[i], _ = json.Marshal(fields)
	}
	if m["messages"], err = json.Marshal(list); err != nil {
		return nil, nil, err
	}
	out2, err := json.Marshal(m)
	return out2, e.losses, err
}

// repairTurns makes the repairs RepairTurns describes on msgs, reporting each in e.
func (e *encoder) repairTurns(msgs []*outMessage, model string) ([]*outMessage, error) {
	msgs = e.pairToolCalls(msgs)
	e.fillEmptyUserTurns(msgs)
	if refusesPrefill(model) {
		n := len(msgs)
		for n > 0 && msgs[n-1].role == llm.RoleAssistant && !(n == len(msgs) && pausedOnServerTool(msgs[n-1])) {
			e.lose(llm.LossDropped, msgs[n-1].path, "this model refuses a conversation that ends on an assistant turn (prefill)")
			n--
		}
		if n < len(msgs) {
			msgs = msgs[:n]
			if !slicesHasTurn(msgs) {
				return nil, invalid("nothing is left to send once the trailing assistant turn is dropped: this model refuses prefill")
			}
		}
	}
	return msgs, nil
}

// slicesHasTurn reports whether msgs holds a user or assistant message.
func slicesHasTurn(msgs []*outMessage) bool {
	for _, m := range msgs {
		if m.role == llm.RoleUser || m.role == llm.RoleAssistant {
			return true
		}
	}
	return false
}

// pausedOnServerTool reports whether an assistant message ends with a server tool call, as a turn
// paused by the server (stop reason pause_turn) does.
func pausedOnServerTool(m *outMessage) bool {
	return len(m.content) > 0 && blockType(m.content[len(m.content)-1]) == "server_tool_use"
}

type blockFields struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func readBlock(b json.RawMessage) blockFields {
	var f blockFields
	_ = json.Unmarshal(b, &f)
	return f
}

func blockType(b json.RawMessage) string { return readBlock(b).Type }

func textBlock(s string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"type": "text", "text": s})
	return b
}

// pairToolCalls answers each tool call left without a result and turns each tool result that
// answers no call into text (RepairTurns).
func (e *encoder) pairToolCalls(msgs []*outMessage) []*outMessage {
	out := make([]*outMessage, 0, len(msgs))
	for i := 0; i < len(msgs); {
		m := msgs[i]
		if m.role != llm.RoleUser {
			out = append(out, m)
			i++
			if m.role != llm.RoleAssistant || i == len(msgs) || msgs[i].role == llm.RoleUser {
				continue
			}
			// The next turn is not the user's: the calls get their results in a user turn of
			// their own.
			if calls := e.unanswered(m, nil); len(calls) > 0 {
				out = append(out, &outMessage{role: llm.RoleUser, content: calls, paths: make([]string, len(calls)), path: m.path, changed: true})
			}
			continue
		}
		// A run of user messages, which Claude reads as one turn.
		j := i
		for j < len(msgs) && msgs[j].role == llm.RoleUser {
			j++
		}
		var prev *outMessage
		if len(out) > 0 && out[len(out)-1].role == llm.RoleAssistant {
			prev = out[len(out)-1]
		}
		out = append(out, e.pairRun(prev, msgs[i:j])...)
		i = j
	}
	return out
}

// toolUseIDs returns the ids of an assistant message's tool calls, in order.
func toolUseIDs(m *outMessage) []string {
	var ids []string
	if m == nil {
		return nil
	}
	for _, b := range m.content {
		if f := readBlock(b); f.Type == "tool_use" && f.ID != "" {
			ids = append(ids, f.ID)
		}
	}
	return ids
}

// unanswered returns an error result for each tool call of the assistant message call that is not
// in answered, and reports each.
func (e *encoder) unanswered(call *outMessage, answered map[string]bool) []json.RawMessage {
	if call == nil {
		return nil
	}
	var results []json.RawMessage
	for j, b := range call.content {
		f := readBlock(b)
		if f.Type != "tool_use" || f.ID == "" || answered[f.ID] {
			continue
		}
		r, _ := json.Marshal(map[string]any{"type": "tool_result", "tool_use_id": f.ID, "is_error": true, "content": interruptedResult})
		results = append(results, r)
		path := call.path
		if j < len(call.paths) && call.paths[j] != "" {
			path = call.paths[j]
		}
		e.lose(llm.LossApproximated, path, "a tool call the next user turn has no result for, which Claude refuses; sent with an error result")
	}
	return results
}

// pairRun repairs a run of user messages that follows the assistant message prev (nil when the
// turn before is not the assistant's). It returns the run as it came when it needs no change, or
// else as one message.
func (e *encoder) pairRun(prev *outMessage, run []*outMessage) []*outMessage {
	ids := map[string]bool{}
	for _, id := range toolUseIDs(prev) {
		ids[id] = true
	}
	answered := map[string]bool{}
	var results, rest []json.RawMessage
	var resultPaths, restPaths []string
	changed, seenOther := false, false
	for _, m := range run {
		for k, b := range m.content {
			path := m.path
			if k < len(m.paths) && m.paths[k] != "" {
				path = m.paths[k]
			}
			f := readBlock(b)
			if f.Type != "tool_result" {
				seenOther = true
				rest = append(rest, b)
				restPaths = append(restPaths, path)
				continue
			}
			if !ids[f.ToolUseID] || answered[f.ToolUseID] {
				changed, seenOther = true, true
				e.lose(llm.LossApproximated, path, "a tool result that answers no tool call in the assistant turn before it, which Claude refuses; sent as text")
				text := resultText(f.Content)
				if len(text) == 0 {
					text = []json.RawMessage{textBlock(emptyToolResult)}
				}
				for range text {
					restPaths = append(restPaths, path)
				}
				rest = append(rest, text...)
				continue
			}
			if seenOther {
				changed = true
				e.lose(llm.LossApproximated, path, "a tool result after other content, which Claude refuses; moved to the start of its user turn")
			}
			answered[f.ToolUseID] = true
			results = append(results, b)
			resultPaths = append(resultPaths, path)
		}
	}
	missing := e.unanswered(prev, answered)
	if len(missing) == 0 && !changed {
		return run
	}
	content := append(append(results, missing...), rest...)
	paths := append(append(resultPaths, make([]string, len(missing))...), restPaths...)
	return []*outMessage{{role: llm.RoleUser, content: content, paths: paths, path: run[0].path, raw: run[0].raw, changed: true}}
}

// resultText returns a tool result's content as user content: a string as a text block, a list
// as its blocks (a block without a type as text), leaving out blank text.
func resultText(content json.RawMessage) []json.RawMessage {
	var s string
	if json.Unmarshal(content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []json.RawMessage{textBlock(s)}
	}
	var list []map[string]json.RawMessage
	if json.Unmarshal(content, &list) != nil {
		return nil
	}
	var out []json.RawMessage
	for _, b := range list {
		if _, ok := b["type"]; !ok {
			b["type"] = json.RawMessage(`"text"`)
		}
		raw, _ := json.Marshal(b)
		if blank(raw) {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// blank reports whether a block is a text block with no visible text, which Claude refuses.
func blank(b json.RawMessage) bool {
	f := readBlock(b)
	return f.Type == "text" && (f.Text == nil || strings.TrimSpace(*f.Text) == "")
}

// fillEmptyUserTurns drops blank text blocks from user turns, and gives a user turn left with
// nothing a placeholder text.
func (e *encoder) fillEmptyUserTurns(msgs []*outMessage) {
	for _, m := range msgs {
		if m.role != llm.RoleUser {
			continue
		}
		var kept []json.RawMessage
		var keptPaths, droppedPaths []string
		for k, b := range m.content {
			path := m.path
			if k < len(m.paths) {
				path = m.paths[k]
			}
			if blank(b) {
				droppedPaths = append(droppedPaths, path)
				continue
			}
			kept = append(kept, b)
			keptPaths = append(keptPaths, path)
		}
		switch {
		case len(kept) == 0:
			m.content, m.paths, m.changed = []json.RawMessage{textBlock(emptyMessage)}, []string{m.path}, true
			e.lose(llm.LossApproximated, m.path, "an empty user turn, which Claude refuses; sent with placeholder text")
		case len(droppedPaths) > 0:
			m.content, m.paths, m.changed = kept, keptPaths, true
			for _, p := range droppedPaths {
				e.lose(llm.LossDropped, p, "blank text, which Claude refuses")
			}
		}
	}
}
