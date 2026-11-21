package anthropicmessages

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	mediainput "github.com/stdotwork/pluxy/next/format/internal/media"
	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// defaultMaxTokens is max_tokens when the client set no output limit: Messages requires one.
// It is today's core's value for clients in other formats.
const defaultMaxTokens = 32000

type encoder struct {
	losses []llm.Loss
	// keepModelTurn: the conversation's trailing assistant turns are kept (EncodeOptions).
	keepModelTurn bool
	// searchResultOwned reports whether a replayed web search result was minted by this request's
	// account (EncodeOptions); nil keeps every result.
	searchResultOwned func(string) bool
}

func (e *encoder) lose(kind, path, detail string) {
	e.losses = append(e.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// kept returns o's kept fields when o came from Messages, so they are written back; for another
// format it carries a valid cache_control field and reports any other kept fields as dropped.
func (e *encoder) kept(o *llm.Origin) json.RawMessage {
	if o == nil {
		return nil
	}
	if o.Format == format {
		return o.Raw
	}
	if m := rawjson.Object(o.Raw); m != nil {
		if cc, ok := validCacheControl(m["cache_control"]); ok {
			if rest := rawjson.Rest(m, "cache_control"); len(rest) > 0 {
				r := *o
				r.Raw = rest
				e.losses = append(e.losses, rawjson.Losses(&r, format)...)
			}
			return rawjson.Encode(map[string]json.RawMessage{"cache_control": cc})
		}
	}
	e.losses = append(e.losses, rawjson.Losses(o, format)...)
	return nil
}

// validCacheControl reports whether raw is a Messages ephemeral cache_control object (with optional
// ttl "5m" or "1h") and returns its trimmed JSON.
func validCacheControl(raw json.RawMessage) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}
	var obj map[string]any
	if json.Unmarshal(trimmed, &obj) != nil || obj == nil {
		return nil, false
	}
	typ, _ := obj["type"].(string)
	if typ != "ephemeral" {
		return nil, false
	}
	if ttlVal, hasTTL := obj["ttl"]; hasTTL && ttlVal != nil {
		ttlStr, ok := ttlVal.(string)
		if !ok || (ttlStr != "5m" && ttlStr != "1h") {
			return nil, false
		}
	}
	return trimmed, true
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
	// paths names where each content block came from in the source body, and path the message.
	paths []string
	path  string
	// raw is the message as a Messages client sent it (RepairTurns); changed says its content
	// was repaired, so it is written again.
	raw     json.RawMessage
	changed bool
	// joinNext: the next item of the same role goes in this message too. A replayed thinking
	// block opens the assistant turn its tool calls and text belong to.
	joinNext bool
}

// EncodeRequest builds a Messages request body from req's items, tools and options, and returns
// what it could not carry over. System and developer messages become the system prompt; the
// items of one source turn (a message split into text, tool calls and results) become one
// message, in order; tool results go in a user message. Function names Messages does not take
// are sent under the names ToolNames gives them, and each tool's parameter schema is made one
// Messages takes (an object schema without a root union). A tool choice that names the tools the
// model may call (an allowed list) is carried by defining only those function tools, since
// Messages has no such list; every other tool, hosted ones included, is dropped, and an allowed
// name the request does not define beside them is reported as dropped. A list that leaves no tool
// to define is an invalid_request error: nothing would hold the model to it. Reasoning from
// another provider cannot be replayed to a Messages upstream (its signatures do not verify), so it
// is dropped and reported, as is the reasoning effort (thinking budgets are model-specific; the
// Claude provider sets them). A user turn left with nothing to send after its unsupported parts
// are dropped is an invalid_request error, never sent empty.
//
// For a Claude model (a claude- name), system and developer messages after the conversation has
// started stay where they are, so the prefix Claude caches stays the same when a client adds one:
// placeSystem says how. Other models get every system message in the system prompt. The
// conversation then gets the repairs RepairTurns describes, each reported. It does not look at
// req.Native; callers that can pass the native body through do so first.
func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	return EncodeRequestWith(req, EncodeOptions{})
}

// EncodeOptions changes how EncodeRequestWith builds a body.
type EncodeOptions struct {
	// KeepModelTurn keeps the conversation's trailing assistant turns on every model: they are
	// the end of the conversation, not a prefill the reply continues. It is for requests that
	// Anthropic takes ending on the model's turn, such as on-demand compaction; a generation
	// leaves it off. Every other repair still runs.
	KeepModelTurn bool
	// SearchResultOwned, when set, reports whether a replayed web search result's
	// encrypted_content was minted by the account this request goes to. Anthropic refuses a
	// result minted for another credential, so an entry it reports false for is left out and the
	// result block keeps an empty list. nil keeps every result, as EncodeRequest does.
	SearchResultOwned func(encryptedContent string) bool
}

// EncodeRequestWith builds a Messages request body as EncodeRequest does, changed by opts.
func EncodeRequestWith(req llm.Request, opts EncodeOptions) ([]byte, []llm.Loss, error) {
	e := &encoder{keepModelTurn: opts.KeepModelTurn, searchResultOwned: opts.SearchResultOwned}
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
	if rf := o.ResponseFormat; rf != nil && rf.Type != "" && rf.Type != llm.ResponseFormatText {
		switch {
		case rf.Type == llm.ResponseFormatJSONSchema && rawjson.Object(rf.Schema) != nil:
			out["output_config"] = map[string]any{
				"format": map[string]any{
					"type":   "json_schema",
					"schema": rf.Schema,
				},
			}
		case rf.Type == llm.ResponseFormatJSONSchema:
			e.lose(llm.LossDropped, "response_format", "Messages output_config.format json_schema requires a schema object")
		default:
			e.lose(llm.LossDropped, "response_format", "Messages output_config.format only supports json_schema")
		}
	}

	names := ToolNames(req)
	// Messages has no allowed tool list: a choice that names the tools the model may call is
	// carried by defining those tools alone, which is what the list asks for. Every tool the
	// list does not name is off the request, hosted and native ones included, since the list
	// says which tools the model may call; nothing is reported for them, as the client's
	// constraint is kept, not approximated.
	allowed := map[string]bool{}
	if tc := o.ToolChoice; tc != nil {
		for _, name := range tc.Allowed {
			allowed[name] = true
		}
	}
	var tools []json.RawMessage
	for i, t := range req.Tools {
		if len(allowed) > 0 && (t.Type == llm.ToolNative || !allowed[t.Name]) {
			continue
		}
		if t.Type == llm.ToolNative {
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
			} else if ws, skip, ok := webSearchTool(t); ok {
				if !skip {
					tools = append(tools, ws)
				}
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
	if len(allowed) > 0 {
		defined := map[string]bool{}
		for _, t := range req.Tools {
			if t.Type != llm.ToolNative {
				defined[t.Name] = true
			}
		}
		var missing []string
		seen := map[string]bool{}
		for _, name := range o.ToolChoice.Allowed {
			if !defined[name] && !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
		}
		if len(tools) == 0 {
			// Nothing is defined to narrow to, so the request cannot say which tools the model
			// may call; sending it would let the model call any of them, or answer with text.
			return nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: fmt.Sprintf("tool_choice allows no function tool this request defines: %s", strings.Join(missing, ", "))}
		}
		if len(missing) > 0 {
			e.lose(llm.LossDropped, "tool_choice", fmt.Sprintf("allowed function tools this request does not define: %s", strings.Join(missing, ", ")))
		}
	}
	if len(tools) > 0 {
		out["tools"] = tools
		if tc := encodeToolChoice(o.ToolChoice, o.ParallelToolCalls, names); tc != nil {
			out["tool_choice"] = tc
		}
	}

	searches := scanWebSearches(req.Items)
	var system []json.RawMessage
	var msgs []*outMessage
	add := func(role, turn, path string, block json.RawMessage) {
		if n := len(msgs); n > 0 && msgs[n-1].role == role && (msgs[n-1].turn == turn || msgs[n-1].joinNext) {
			msgs[n-1].joinNext = false
			msgs[n-1].content = append(msgs[n-1].content, block)
			msgs[n-1].paths = append(msgs[n-1].paths, path)
			return
		}
		msgs = append(msgs, &outMessage{role: role, turn: turn, path: turn, content: []json.RawMessage{block}, paths: []string{path}})
	}
	claude := strings.HasPrefix(claudeName(req.Model), "claude-")
	for i, it := range req.Items {
		p := pathOf(it.Origin, fmt.Sprintf("items[%d]", i))
		turn := turnOf(it, i)
		switch it.Type {
		case llm.ItemTypeMessage:
			if len(it.Content) == 0 && !it.TextContent && it.Origin != nil && it.Origin.Format != format && len(it.Origin.Raw) > 0 {
				// An empty message from another format only holds its own fields; they are
				// reported, and nothing is sent for it.
				e.losses = append(e.losses, rawjson.Losses(it.Origin, format)...)
				break
			}
			blocks, paths, sendable, dropped := e.content(it, p)
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to Messages", p)
			}
			if it.Origin != nil && it.Origin.Format != format {
				if len(blocks) > 0 {
					if raw := e.kept(it.Origin); len(raw) > 0 && rawjson.Object(blocks[len(blocks)-1])["cache_control"] == nil {
						blocks[len(blocks)-1] = rawjson.Merge(blocks[len(blocks)-1], raw)
					}
				} else {
					e.losses = append(e.losses, rawjson.Losses(it.Origin, format)...)
				}
			}
			switch {
			case (it.Role == llm.RoleSystem || it.Role == llm.RoleDeveloper) && claude && len(msgs) > 0:
				for _, b := range blocks {
					add(roleSystem, turn, p, b)
				}
			case it.Role == llm.RoleSystem || it.Role == llm.RoleDeveloper:
				system = append(system, blocks...)
			default:
				for k, b := range blocks {
					add(it.Role, turn, paths[k], b)
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
			add(llm.RoleAssistant, turn, p, marshal(map[string]any{"type": "tool_use", "id": it.CallID, "name": toolName(names, it.Name), "input": input}, e.kept(it.Origin)))
		case llm.ItemTypeToolResult:
			m := map[string]any{"type": "tool_result", "tool_use_id": it.CallID, "content": it.Output}
			if len(it.Content) > 0 {
				blocks, _, _, _ := e.content(llm.Item{Role: llm.RoleUser, Content: it.Content}, p)
				if len(blocks) > 0 {
					m["content"] = blocks
				}
			}
			if it.IsError {
				m["is_error"] = true
			}
			add(llm.RoleUser, turn, p, marshal(m, e.kept(it.Origin)))
		case llm.ItemTypeReasoning:
			if it.Origin != nil && it.Origin.Format == format {
				add(llm.RoleAssistant, turn, p, marshal(map[string]any{"type": "thinking", "thinking": it.Text}, it.Origin.Raw))
				break
			}
			if sig := replayedSignature(it); sig != "" {
				add(llm.RoleAssistant, turn, p, marshal(map[string]any{"type": "thinking", "thinking": it.Text, "signature": sig}, nil))
				msgs[len(msgs)-1].joinNext = true
				break
			}
			e.lose(llm.LossDropped, p, "Messages cannot replay reasoning from another provider")
		case llm.ItemTypeNative:
			if it.Origin != nil && it.Origin.Format == format {
				add(llm.RoleAssistant, turn, p, it.Origin.Raw)
				break
			}
			if c, ok := asWebSearchCall(it); ok {
				// A search goes in the assistant message around it, with the text and calls
				// before and after it, as Claude wrote that turn.
				blocks, dropped := searches.blocks(i, c, e.searchResultOwned)
				if dropped > 0 {
					e.lose(llm.LossDropped, p, "web search results this account did not mint (Anthropic refuses another credential's encrypted content); the result block is sent with an empty list")
				}
				if n := len(msgs); n > 0 && msgs[n-1].role == llm.RoleAssistant && len(blocks) > 0 {
					msgs[n-1].joinNext = true
				}
				for _, b := range blocks {
					add(llm.RoleAssistant, turn, p, b)
				}
				if len(blocks) > 0 {
					msgs[len(msgs)-1].joinNext = true
				}
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
	msgs = e.placeSystem(msgs, takesMidSystem(req.Model))
	msgs, err := e.repairTurns(msgs, req.Model)
	if err != nil {
		return nil, nil, err
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

// content encodes a message's parts as Messages content blocks, with the path each came from.
// sendable reports whether any block with something in it was kept, dropped whether any part was
// dropped and reported.
func (e *encoder) content(it llm.Item, msgPath string) (blocks []json.RawMessage, paths []string, sendable, dropped bool) {
	for i, p := range it.Content {
		pp := pathOf(p.Origin, fmt.Sprintf("%s.content[%d]", msgPath, i))
		var block map[string]any
		switch p.Type {
		case llm.PartText:
			if p.Text == "" {
				// Messages rejects empty text blocks.
				e.losses = append(e.losses, rawjson.Losses(p.Origin, format)...)
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
				paths = append(paths, pp)
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
		paths = append(paths, pp)
		sendable = true
	}
	return blocks, paths, sendable, dropped
}

// source returns the Messages source of an image or file part: inline base64 from a data URL, a
// URL, or a file id. Documents inline take PDF or plain text only.
func source(p llm.Part) map[string]any {
	url := p.URL
	if p.Type == llm.PartFile && p.FileData != "" {
		url = p.FileData
	}
	if p.Type == llm.PartFile && p.FileData != "" && (p.Origin == nil || p.Origin.Format != format) && !mediainput.FileDataOK(p.FileData) {
		return nil
	}
	switch {
	case mediainput.IsDataURL(url):
		mediaType, data, ok := mediainput.DataURL(url)
		if !ok {
			return nil
		}
		if p.Origin == nil || p.Origin.Format != format {
			// Only a part translated from another format is checked; one the client sent to
			// Messages is what Messages accepts, and its media type goes back as written.
			mediaType = mediainput.Type(mediaType)
			if !mediainput.Base64(data) {
				return nil
			}
			if p.Type == llm.PartImage || strings.HasPrefix(mediaType, "image/") {
				if _, problem := mediainput.InlineImage(url, mediainput.ClaudeImages); problem != "" {
					return nil
				}
			}
			if p.Type == llm.PartFile && !strings.HasPrefix(mediaType, "image/") && mediaType != "application/pdf" {
				return nil
			}
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
// shortcut: a Claude signature is told apart by its shape (claudeSignature); an origin record that
// travels with the item would be exact.
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
	if !claudeSignature(r.EncryptedContent) {
		return ""
	}
	return r.EncryptedContent
}

// claudeSignature reports whether sig has the shape of a thinking signature Claude issues: base64
// of a protobuf message. The older forms start with E (first byte 0x12, field 2) or R (base64 of
// such a string), as today's core checks. Claude 5 models issue a newer envelope that starts with C:
// field 1, a one-byte version varint, then field 2 (0x08, version, 0x12), the layout CLIProxyAPI
// describes for it (internal/signature/claude_validation.go, "CAIS envelope").
func claudeSignature(sig string) bool {
	if sig == "" || (sig[0] != 'E' && sig[0] != 'R' && sig[0] != 'C') {
		return false
	}
	b, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(sig); err != nil {
			return false
		}
	}
	return sig[0] != 'C' || (len(b) > 2 && b[0] == 0x08 && b[1] < 0x80 && b[2] == 0x12)
}
