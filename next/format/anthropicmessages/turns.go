package anthropicmessages

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	interruptedResult = "Tool call was interrupted before any output was recorded."
	emptyToolResult   = "(empty tool result)"
	emptyMessage      = "(empty message)"
)

func refusesPrefill(model string) bool {
	if !strings.HasPrefix(claudeName(model), "claude-") {
		return false
	}
	major, minor, ok := parseClaudeVersion(model)
	return !ok || major > 4 || (major == 4 && minor >= 6)
}

var claudeFamily = regexp.MustCompile(`^claude-(opus|sonnet|haiku|fable|mythos)-`)

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

func (e *encoder) repairTurns(msgs []*outMessage, model string) ([]*outMessage, error) {
	msgs = e.pairToolCalls(msgs)
	e.fillEmptyUserTurns(msgs)
	if refusesPrefill(model) && !e.keepModelTurn {
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

func slicesHasTurn(msgs []*outMessage) bool {
	for _, m := range msgs {
		if m.role == llm.RoleUser || m.role == llm.RoleAssistant {
			return true
		}
	}
	return false
}

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

func blank(b json.RawMessage) bool {
	f := readBlock(b)
	return f.Type == "text" && (f.Text == nil || strings.TrimSpace(*f.Text) == "")
}

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
