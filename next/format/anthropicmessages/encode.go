package anthropicmessages

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// defaultMaxTokens is max_tokens when the client set no output limit: Messages requires one.
// It is today's core's value for clients in other formats.
const defaultMaxTokens = 32000

type encoder struct {
	losses []llm.Loss
}

func (e *encoder) lose(kind, path, detail string) {
	e.losses = append(e.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// kept returns o's kept fields when o came from Messages, so they are written back; for another
// format it reports them as dropped.
func (e *encoder) kept(o *llm.Origin) json.RawMessage {
	if o == nil {
		return nil
	}
	if o.Format == format {
		return o.Raw
	}
	e.losses = append(e.losses, rawjson.Losses(o, format)...)
	return nil
}

func pathOf(o *llm.Origin, fallback string) string {
	if o != nil && o.Path != "" {
		return o.Path
	}
	return fallback
}

func marshal(v any, raw json.RawMessage) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return rawjson.Merge(b, raw)
}

// turnOf names the source turn an item came from: the first segment of its origin path (for
// example contents[2]), so the items one turn was split into go back into one message.
func turnOf(it llm.Item, i int) string {
	if it.Origin != nil && it.Origin.Path != "" {
		p, _, _ := strings.Cut(it.Origin.Path, ".")
		return p
	}
	return fmt.Sprintf("items[%d]", i)
}

type outMessage struct {
	role    string
	turn    string
	content []json.RawMessage
	// joinNext: the next item of the same role goes in this message too. A replayed thinking
	// block opens the assistant turn its tool calls and text belong to.
	joinNext bool
}

// EncodeRequest builds a Messages request body from req's items, tools and options, and returns
// what it could not carry over. System and developer messages become the system prompt; the
// items of one source turn (a message split into text, tool calls and results) become one
// message, in order; tool results go in a user message. Function names Messages does not take
// are sent under the names ToolNames gives them, and each tool's parameter schema is made one
// Messages takes (an object schema without a root union). Reasoning from another provider cannot be
// replayed to a Messages upstream (its signatures do not verify), so it is dropped and reported,
// as is the reasoning effort (thinking budgets are model-specific; the Claude provider sets
// them). A user turn left with nothing to send after its unsupported parts are dropped is an
// invalid_request error, never sent empty. It does not look at req.Native; callers that can pass
// the native body through do so first.
func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	e := &encoder{}
	o := req.Options
	out := map[string]any{"model": req.Model, "stream": req.Stream}
	if o.MaxOutputTokens != nil {
		out["max_tokens"] = *o.MaxOutputTokens
	} else {
		out["max_tokens"] = defaultMaxTokens
	}
	if o.Temperature != nil {
		out["temperature"] = *o.Temperature
	}
	if o.TopP != nil {
		out["top_p"] = *o.TopP
	}
	if len(o.Stop) > 0 {
		out["stop_sequences"] = o.Stop
	}
	if o.ReasoningEffort != "" {
		e.lose(llm.LossDropped, "reasoning_effort", "Messages takes a thinking budget, which depends on the model")
	}
	if o.ResponseFormat != nil && o.ResponseFormat.Type != llm.ResponseFormatText {
		e.lose(llm.LossDropped, "response_format", "Messages has no response format")
	}

	names := ToolNames(req)
	var tools []json.RawMessage
	for i, t := range req.Tools {
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
			} else {
				e.lose(llm.LossDropped, pathOf(t.Origin, fmt.Sprintf("tools[%d]", i)), "Messages has no such tool")
			}
			continue
		}
		ft := map[string]any{"name": toolName(names, t.Name), "input_schema": inputSchema(t.Parameters)}
		if t.Description != "" {
			ft["description"] = t.Description
		}
		if t.Strict != nil {
			ft["strict"] = *t.Strict
		}
		tools = append(tools, marshal(ft, e.kept(t.Origin)))
	}
	if len(tools) > 0 {
		out["tools"] = tools
		if tc := encodeToolChoice(o.ToolChoice, o.ParallelToolCalls, names); tc != nil {
			out["tool_choice"] = tc
		}
	}

	var system []json.RawMessage
	var msgs []*outMessage
	add := func(role, turn string, block json.RawMessage) {
		if n := len(msgs); n > 0 && msgs[n-1].role == role && (msgs[n-1].turn == turn || msgs[n-1].joinNext) {
			msgs[n-1].joinNext = false
			msgs[n-1].content = append(msgs[n-1].content, block)
			return
		}
		msgs = append(msgs, &outMessage{role: role, turn: turn, content: []json.RawMessage{block}})
	}
	for i, it := range req.Items {
		p := pathOf(it.Origin, fmt.Sprintf("items[%d]", i))
		turn := turnOf(it, i)
		switch it.Type {
		case llm.ItemTypeMessage:
			if len(it.Content) == 0 && !it.TextContent && it.Origin != nil && it.Origin.Format != format && len(it.Origin.Raw) > 0 {
				// An empty message from another format only holds its own fields; they are
				// reported, and nothing is sent for it.
				e.kept(it.Origin)
				break
			}
			e.kept(it.Origin)
			blocks, sendable, dropped := e.content(it, p)
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to Messages", p)
			}
			switch it.Role {
			case llm.RoleSystem, llm.RoleDeveloper:
				system = append(system, blocks...)
			default:
				for _, b := range blocks {
					add(it.Role, turn, b)
				}
			}
		case llm.ItemTypeToolCall:
			if it.Custom {
				e.lose(llm.LossDropped, p, "Messages has no custom tool calls")
				break
			}
			input := json.RawMessage(it.Arguments)
			var m map[string]json.RawMessage
			if json.Unmarshal(input, &m) != nil || m == nil {
				if strings.TrimSpace(it.Arguments) != "" {
					e.lose(llm.LossApproximated, p, "tool arguments that are not a JSON object, sent as {}")
				}
				input = json.RawMessage("{}")
			}
			add(llm.RoleAssistant, turn, marshal(map[string]any{"type": "tool_use", "id": it.CallID, "name": toolName(names, it.Name), "input": input}, e.kept(it.Origin)))
		case llm.ItemTypeToolResult:
			add(llm.RoleUser, turn, marshal(map[string]any{"type": "tool_result", "tool_use_id": it.CallID, "content": it.Output}, e.kept(it.Origin)))
		case llm.ItemTypeReasoning:
			if it.Origin != nil && it.Origin.Format == format {
				add(llm.RoleAssistant, turn, marshal(map[string]any{"type": "thinking", "thinking": it.Text}, it.Origin.Raw))
				break
			}
			if sig := replayedSignature(it); sig != "" {
				add(llm.RoleAssistant, turn, marshal(map[string]any{"type": "thinking", "thinking": it.Text, "signature": sig}, nil))
				msgs[len(msgs)-1].joinNext = true
				break
			}
			e.lose(llm.LossDropped, p, "Messages cannot replay reasoning from another provider")
		case llm.ItemTypeNative:
			if it.Origin != nil && it.Origin.Format == format {
				add(llm.RoleAssistant, turn, it.Origin.Raw)
				break
			}
			e.lose(llm.LossDropped, p, "Messages has no such item")
		default:
			return nil, nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("%s: unsupported item type %q", p, it.Type), Scope: llm.ScopeRequest}
		}
	}
	if len(system) > 0 {
		out["system"] = system
	}
	messages := make([]any, 0, len(msgs))
	for _, m := range msgs {
		messages = append(messages, map[string]any{"role": m.role, "content": m.content})
	}
	out["messages"] = messages
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, err
	}
	return rawjson.Merge(b, e.kept(req.Origin)), e.losses, nil
}

func encodeToolChoice(tc *llm.ToolChoice, parallel *bool, names map[string]string) map[string]any {
	var out map[string]any
	if tc != nil {
		switch tc.Mode {
		case llm.ToolChoiceAuto:
			out = map[string]any{"type": "auto"}
		case llm.ToolChoiceNone:
			out = map[string]any{"type": "none"}
		case llm.ToolChoiceRequired:
			out = map[string]any{"type": "any"}
		case llm.ToolChoiceTool:
			out = map[string]any{"type": "tool", "name": toolName(names, tc.Name)}
		}
	}
	if parallel != nil && !*parallel {
		if out == nil {
			out = map[string]any{"type": "auto"}
		}
		if out["type"] != "none" {
			out["disable_parallel_tool_use"] = true
		}
	}
	return out
}

// content encodes a message's parts as Messages content blocks. sendable reports whether any
// block with something in it was kept, dropped whether any part was dropped and reported.
func (e *encoder) content(it llm.Item, msgPath string) (blocks []json.RawMessage, sendable, dropped bool) {
	for i, p := range it.Content {
		pp := pathOf(p.Origin, fmt.Sprintf("%s.content[%d]", msgPath, i))
		var block map[string]any
		switch p.Type {
		case llm.PartText:
			if p.Text == "" {
				// Messages rejects empty text blocks.
				e.kept(p.Origin)
				continue
			}
			block = map[string]any{"type": "text", "text": p.Text}
		case llm.PartImage, llm.PartFile:
			src := source(p)
			if src == nil {
				e.lose(llm.LossDropped, pp, "Messages cannot take this "+p.Type)
				dropped = true
				continue
			}
			typ := "image"
			if p.Type == llm.PartFile {
				typ = "document"
				if mt, _ := src["media_type"].(string); src["type"] == "base64" && strings.HasPrefix(mt, "image/") {
					typ = "image"
				}
			}
			block = map[string]any{"type": typ, "source": src}
			if typ == "document" && p.Filename != "" {
				block["title"] = p.Filename
			}
		case llm.PartNative:
			if p.Format == format {
				blocks = append(blocks, p.Data)
				sendable = true
				continue
			}
			e.lose(llm.LossDropped, pp, "a "+p.Format+" content part Messages cannot carry")
			dropped = true
			continue
		default:
			e.lose(llm.LossDropped, pp, "a "+p.Type+" content part Messages cannot carry")
			dropped = true
			continue
		}
		blocks = append(blocks, marshal(block, e.kept(p.Origin)))
		sendable = true
	}
	return blocks, sendable, dropped
}

// source returns the Messages source of an image or file part: inline base64 from a data URL, a
// URL, or a file id. Documents inline take PDF or plain text only.
func source(p llm.Part) map[string]any {
	url := p.URL
	if p.Type == llm.PartFile && p.FileData != "" {
		url = p.FileData
	}
	switch {
	case strings.HasPrefix(url, "data:"):
		meta, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
		mediaType, isBase64 := strings.CutSuffix(meta, ";base64")
		if !ok || !isBase64 {
			return nil
		}
		if p.Type == llm.PartFile && !strings.HasPrefix(mediaType, "image/") && mediaType != "application/pdf" {
			return nil
		}
		return map[string]any{"type": "base64", "media_type": mediaType, "data": data}
	case url != "":
		return map[string]any{"type": "url", "url": url}
	case p.FileID != "":
		return map[string]any{"type": "file", "file_id": p.FileID}
	}
	return nil
}

// replayedSignature returns the Claude thinking signature a Responses reasoning item carries in
// encrypted_content, or "" when it carries none. The Responses writer puts a Claude signature
// there unchanged; OpenAI's own encrypted content is a Fernet token, which starts with "g".
//
// shortcut: a Claude signature is told apart by its shape (base64 starting with "E" or "R", as
// today's core checks); an origin record that travels with the item would be exact.
func replayedSignature(it llm.Item) string {
	if it.Origin == nil || it.Origin.Format != llm.FormatOpenAIResponses {
		return ""
	}
	var r struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal(it.Origin.Raw, &r) != nil {
		return ""
	}
	sig := r.EncryptedContent
	if sig == "" || (sig[0] != 'E' && sig[0] != 'R') {
		return ""
	}
	if _, err := base64.StdEncoding.DecodeString(sig); err != nil {
		if _, err := base64.RawStdEncoding.DecodeString(sig); err != nil {
			return ""
		}
	}
	return sig
}
