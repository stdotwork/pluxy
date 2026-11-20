package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
)

// The Codex endpoint refuses some conversations that other clients, other accounts or the Codex
// client's own features produce. history repairs them before the request is sent, and reports
// each change as a loss at the item's path in the request's input:
//   - a tool call with no output anywhere in the conversation (a session that stopped while the
//     tool ran) gets an interrupted output right after it, as the Codex client's own history
//     repair places one ("No tool output found for function call" otherwise);
//   - an item_reference names a stored item, and Codex never stores a response (store is always
//     false), so over HTTP it is dropped; over the WebSocket, where a turn continues the socket's
//     previous response, it is left for the upstream;
//   - an agent_message, which the Codex client sends for its subagents, goes to a subscription as
//     it came; a SIWC account refuses it (subscription_sharing_unsupported_capability), and every
//     Codex upstream refuses one without its author or recipient, so those go as user messages.

// interruptedOutput is the output sent for a tool call that has none, as the Claude repairs word
// it.
const interruptedOutput = "Tool call was interrupted before any output was recorded."

// outputTypes maps each tool call type to the type of the output that answers it. A local shell
// call is answered by a function call output, as the Codex client records it.
var outputTypes = map[string]string{
	"function_call":    "function_call_output",
	"local_shell_call": "function_call_output",
	"custom_tool_call": "custom_tool_call_output",
	"tool_search_call": "tool_search_output",
}

// history makes the repairs described above on the request's input, which input has made a list.
// It also records the paths of the reasoning items whose encrypted content another account may
// have made (see dropForeignReasoning).
func (h *shaper) history() {
	var raws []json.RawMessage
	if json.Unmarshal(h.m["input"], &raws) != nil {
		return
	}
	objs := make([]map[string]json.RawMessage, len(raws))
	answered := map[string]bool{} // output type + "\x00" + call ID
	for i, r := range raws {
		_ = json.Unmarshal(r, &objs[i]) // an item that is not an object stays nil
		typ, _ := str(objs[i]["type"])
		if id, _ := str(objs[i]["call_id"]); id != "" {
			answered[typ+"\x00"+id] = true
		}
	}
	out := make([]json.RawMessage, 0, len(raws))
	edited := false
	for i, r := range raws {
		o := objs[i]
		path := fmt.Sprintf("input[%d]", i)
		typ, _ := str(o["type"])
		switch typ {
		case "item_reference":
			if !h.ws {
				h.lose(llm.LossDropped, path, "an item_reference, which Codex cannot resolve: it stores no response")
				edited = true
				continue
			}
		case "agent_message":
			if msg, changed := h.agentMessage(o, path); changed {
				edited = true
				if msg == nil {
					continue
				}
				r = msg
			}
		case "reasoning":
			if enc, _ := str(o["encrypted_content"]); enc != "" {
				if unsendableReasoning(o) {
					// limits leaves it out.
					h.lose(llm.LossDropped, path, "a reasoning item whose ID is over Codex's limit; its encrypted content cannot be read under another ID")
				} else {
					h.encryptedReasoning = append(h.encryptedReasoning, path)
				}
			}
		}
		out = append(out, r)
		if output := missingOutput(typ, o, answered); output != nil {
			out = append(out, output)
			edited = true
			h.lose(llm.LossApproximated, path, "a tool call with no output, which Codex refuses; sent with an interrupted output")
		}
	}
	if edited {
		h.m["input"] = raw(out)
	}
}

func (h *shaper) lose(kind, path, detail string) {
	h.tr.Losses = llm.AddLoss(h.tr.Losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// missingOutput returns the output to send after a tool call of type typ (the item o) that the
// conversation has no output for, and marks the call answered; nil for any other item.
func missingOutput(typ string, o map[string]json.RawMessage, answered map[string]bool) json.RawMessage {
	outType, ok := outputTypes[typ]
	id, _ := str(o["call_id"])
	if !ok || id == "" || answered[outType+"\x00"+id] {
		return nil
	}
	answered[outType+"\x00"+id] = true
	if outType == "tool_search_output" {
		// A tool search the client ran, which found no tools.
		return raw(map[string]any{"type": outType, "call_id": id, "status": "completed", "execution": "client", "tools": []any{}})
	}
	return raw(map[string]any{"type": outType, "call_id": id, "output": interruptedOutput})
}

// unsendableReasoning reports whether o is a reasoning item with encrypted content whose ID is
// over the endpoint's limit: it cannot be sent as it is, and the endpoint could not read its
// content under another ID, so limits leaves it out.
func unsendableReasoning(o map[string]json.RawMessage) bool {
	typ, _ := str(o["type"])
	id, ok := str(o["id"])
	enc, _ := str(o["encrypted_content"])
	return typ == "reasoning" && ok && enc != "" && utf8.RuneCountInString(id) > itemIDLimit
}

// agentMessage returns the item to send for the agent_message o, at path in input, and whether it
// differs from o: o itself when the account takes it, a user message with o's readable content
// when it does not, or nil when nothing in it can be read. Encrypted parts can be read only by the
// upstream that made them, so they are left out.
func (h *shaper) agentMessage(o map[string]json.RawMessage, path string) (json.RawMessage, bool) {
	author, _ := str(o["author"])
	recipient, _ := str(o["recipient"])
	var why string
	switch {
	case h.a.siwc:
		why = "an agent_message, which a SIWC account refuses; sent as a user message without its author and recipient"
	case strings.TrimSpace(author) == "" || strings.TrimSpace(recipient) == "":
		why = "an agent_message without its author or recipient, which Codex refuses; sent as a user message"
	default:
		return nil, false
	}
	var parts []json.RawMessage
	if text, ok := str(o["content"]); ok {
		parts = append(parts, raw(map[string]string{"type": "input_text", "text": text}))
	} else {
		var list []json.RawMessage
		_ = json.Unmarshal(o["content"], &list)
		for j, p := range list {
			var part struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(p, &part)
			if part.Type == "encrypted_content" {
				h.lose(llm.LossDropped, fmt.Sprintf("%s.content[%d]", path, j), "encrypted agent message content, which only the upstream that made it can read")
				continue
			}
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		h.lose(llm.LossDropped, path, "an agent_message with nothing readable in it, which this account cannot take")
		return nil, true
	}
	h.lose(llm.LossApproximated, path, why)
	return raw(map[string]any{"type": "message", "role": "user", "content": parts}), true
}

// foreignReasoning reports whether e is Codex refusing encrypted reasoning it cannot read: reasoning
// another account (or another provider's Responses upstream) made, replayed by the client.
func foreignReasoning(e *llm.Error) bool {
	if e == nil || e.Status != 400 {
		return false
	}
	if e.ProviderCode == "invalid_encrypted_content" {
		return true
	}
	msg := strings.ToLower(e.Message)
	return strings.Contains(msg, "encrypted content") && (strings.Contains(msg, "could not be verified") || strings.Contains(msg, "could not be decrypted"))
}

// dropForeignReasoning returns the request's body without its reasoning items that carry encrypted
// content, and reports each as dropped, for one more try after Codex refused that content. It
// returns false when there is no such item to leave out.
func (u *upstreamRequest) dropForeignReasoning() ([]byte, bool) {
	var m map[string]json.RawMessage
	var items []json.RawMessage
	if json.Unmarshal(u.body, &m) != nil || json.Unmarshal(m["input"], &items) != nil {
		return nil, false
	}
	kept := make([]json.RawMessage, 0, len(items))
	for _, it := range items {
		var o struct {
			Type             string `json:"type"`
			EncryptedContent string `json:"encrypted_content"`
		}
		if json.Unmarshal(it, &o) == nil && o.Type == "reasoning" && o.EncryptedContent != "" {
			continue
		}
		kept = append(kept, it)
	}
	if len(kept) == len(items) {
		return nil, false
	}
	m["input"] = raw(kept)
	body, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	paths := u.encryptedReasoning
	if len(paths) == 0 {
		paths = []string{"input"}
	}
	for _, p := range paths {
		u.tr.Losses = llm.AddLoss(u.tr.Losses, llm.Loss{Kind: llm.LossDropped, Path: p,
			Detail: "encrypted reasoning this account cannot read (another account or provider made it); sent again without it"})
	}
	return body, true
}
