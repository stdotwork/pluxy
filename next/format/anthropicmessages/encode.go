package anthropicmessages

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	mediainput "github.com/stdotwork/pluxy/next/format/internal/media"
	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const defaultMaxTokens = 32000

// maxCacheBreakpoints is the maximum number of cache_control breakpoints Anthropic's Messages API
// accepts per request (explicit block breakpoints plus top-level automatic caching).
const maxCacheBreakpoints = 4

type encoder struct {
	losses            []llm.Loss
	keepModelTurn     bool
	searchResultOwned func(string) bool
}

func (e *encoder) lose(kind, path, detail string) {
	e.losses = append(e.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

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

func turnOf(it llm.Item, i int) string {
	if it.Origin != nil && it.Origin.Path != "" {
		p, _, _ := strings.Cut(it.Origin.Path, ".")
		return p
	}
	return fmt.Sprintf("items[%d]", i)
}

type outMessage struct {
	role     string
	turn     string
	content  []json.RawMessage
	paths    []string
	path     string
	raw      json.RawMessage
	changed  bool
	joinNext bool
}

func EncodeRequest(req llm.Request) ([]byte, []llm.Loss, error) {
	return EncodeRequestWith(req, EncodeOptions{})
}

type EncodeOptions struct {
	KeepModelTurn         bool
	SearchResultOwned     func(encryptedContent string) bool
	ServiceTier           bool
	FastMode              bool
	WebSearch             bool
	CodeExecution         bool
	StructuredOutputRules bool
}

func EncodeRequestWith(req llm.Request, opts EncodeOptions) ([]byte, []llm.Loss, error) {
	e := &encoder{keepModelTurn: opts.KeepModelTurn, searchResultOwned: opts.SearchResultOwned}
	req, lowered := clienttools.Lower(req)
	e.losses = append(e.losses, lowered...)
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
	e.serviceTier(req, opts, out)
	if o.Verbosity != "" {
		e.lose(llm.LossDropped, llm.VerbosityPath(req), "Messages has no verbosity setting")
	}
	if o.CandidateCount > 1 {
		e.lose(llm.LossDropped, llm.CandidateCountPath(req), "Messages has no candidate count setting")
	}
	if rf := o.ResponseFormat; rf != nil && rf.Type != "" && rf.Type != llm.ResponseFormatText {
		switch {
		case rf.Type == llm.ResponseFormatJSONSchema && rawjson.Object(rf.Schema) != nil:
			if req.SourceFormat() == format || !opts.StructuredOutputRules {
				out["output_config"] = map[string]any{
					"format": map[string]any{
						"type":   "json_schema",
						"schema": rf.Schema,
					},
				}
			} else if !supportsStructuredOutput(req.Model) {
				e.lose(llm.LossDropped, "response_format", fmt.Sprintf("Messages model %q does not support output_config.format json_schema", req.Model))
			} else if schema, losses, ok := adaptStructuredSchema(rf.Schema); ok {
				e.losses = append(e.losses, losses...)
				out["output_config"] = map[string]any{
					"format": map[string]any{
						"type":   "json_schema",
						"schema": schema,
					},
				}
			} else {
				e.losses = append(e.losses, losses...)
			}
		case rf.Type == llm.ResponseFormatJSONSchema:
			e.lose(llm.LossDropped, "response_format", "Messages output_config.format json_schema requires a schema object")
		default:
			e.lose(llm.LossDropped, "response_format", "Messages output_config.format only supports json_schema")
		}
	}

	names := ToolNames(req)
	allowed := map[string]bool{}
	if tc := o.ToolChoice; tc != nil {
		for _, name := range tc.Allowed {
			allowed[name] = true
		}
	}
	var tools []json.RawMessage
	emittedCodeExecName := ""
	droppedCodeExec := false
	sent := map[string]bool{}
	if len(allowed) == 0 {
		for _, t := range req.Tools {
			if t.IsFunction() {
				sent[toolName(names, t.Name)] = true
			}
		}
	}
	claim := func(tool json.RawMessage, path string) bool {
		var named struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(tool, &named)
		name := named.Name
		if sent[name] {
			e.lose(llm.LossDropped, path, fmt.Sprintf("Messages needs each tool name once and another tool is already named %q", name))
			return false
		}
		sent[name] = true
		return true
	}
	for i, t := range req.Tools {
		if len(allowed) > 0 && (!t.IsFunction() || !allowed[t.Name]) {
			continue
		}
		if t.Type == llm.ToolCodeExecution {
			p := pathOf(t.Origin, fmt.Sprintf("tools[%d]", i))
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
				emittedCodeExecName = cmp.Or(strings.TrimSpace(t.Name), codeExecutionToolName)
			} else if !opts.CodeExecution {
				e.lose(llm.LossDropped, p, llm.ToolCodeExecutionUnsent)
				droppedCodeExec = true
			} else if !supportsCodeExecution(req.Model) {
				e.lose(llm.LossDropped, p, fmt.Sprintf("Messages model %q does not support %s", req.Model, codeExecutionToolType))
				droppedCodeExec = true
			} else if !claim(canonicalCodeExecutionTool(), p) {
				droppedCodeExec = true
			} else {
				tools = append(tools, canonicalCodeExecutionTool())
				emittedCodeExecName = codeExecutionToolName
				e.losses = append(e.losses, rawjson.CodeExecutionLosses(t, p, format)...)
			}
			continue
		}
		if t.Type == llm.ToolWebSearch {
			p := pathOf(t.Origin, fmt.Sprintf("tools[%d]", i))
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
			} else if ws, skip, carried, ok := webSearchTool(t); ok {
				if !skip && claim(ws, p) {
					tools = append(tools, ws)
					for _, l := range rawjson.WebSearchLosses(t, p, format, carried) {
						l.Kind = llm.LossApproximated
						e.losses = append(e.losses, l)
					}
				}
			} else if !opts.WebSearch {
				e.lose(llm.LossDropped, p, llm.WebSearchUnsent)
			} else if t.WebSearch != nil && t.WebSearch.Disabled {
			} else if !supportsWebSearch(req.Model) {
				e.lose(llm.LossDropped, p, fmt.Sprintf("Messages model %q does not support %s", req.Model, webSearchToolType))
			} else if ws, skip, carried, ok := canonicalWebSearchTool(t); ok {
				if !skip && claim(ws, p) {
					tools = append(tools, ws)
					e.losses = append(e.losses, rawjson.WebSearchLosses(t, p, format, carried)...)
				}
			} else {
				e.lose(llm.LossDropped, p, "Messages has no such tool")
			}
			continue
		}
		if t.Type == llm.ToolNative {
			p := pathOf(t.Origin, fmt.Sprintf("tools[%d]", i))
			if t.Origin != nil && t.Origin.Format == format {
				tools = append(tools, t.Origin.Raw)
			} else if ws, skip, carried, ok := webSearchTool(t); ok {
				if !skip && claim(ws, p) {
					tools = append(tools, ws)
					for _, l := range rawjson.WebSearchLosses(t, p, format, carried) {
						l.Kind = llm.LossApproximated
						e.losses = append(e.losses, l)
					}
				}
			} else {
				e.lose(llm.LossDropped, p, "Messages has no such tool")
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
			if t.IsFunction() {
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
			return nil, nil, &llm.Error{Code: llm.CodeInvalidRequest, Status: http.StatusBadRequest, Scope: llm.ScopeRequest,
				Message: fmt.Sprintf("tool_choice allows no function tool this request defines: %s", strings.Join(missing, ", "))}
		}
		if len(missing) > 0 {
			e.lose(llm.LossDropped, "tool_choice", fmt.Sprintf("allowed function tools this request does not define: %s", strings.Join(missing, ", ")))
		}
	}
	if o.ToolChoice != nil && o.ToolChoice.Mode == llm.ToolChoiceCodeExecution {
		if emittedCodeExecName != "" && len(tools) > 0 {
			out["tools"] = tools
			if tc := encodeToolChoice(&llm.ToolChoice{Mode: llm.ToolChoiceTool, Name: emittedCodeExecName}, o.ParallelToolCalls, nil); tc != nil {
				out["tool_choice"] = tc
			}
		} else {
			e.lose(llm.LossDropped, "tool_choice", "Messages cannot force code_execution without a code execution tool")
			if len(tools) > 0 {
				out["tools"] = tools
				if tc := encodeToolChoice(nil, o.ParallelToolCalls, names); tc != nil {
					out["tool_choice"] = tc
				}
			}
		}
	} else if len(tools) > 0 {
		out["tools"] = tools
		if tc := encodeToolChoice(o.ToolChoice, o.ParallelToolCalls, names); tc != nil {
			out["tool_choice"] = tc
		}
	} else if droppedCodeExec && o.ToolChoice != nil && (o.ToolChoice.Mode == llm.ToolChoiceRequired || o.ToolChoice.Mode == llm.ToolChoiceTool) {
		e.lose(llm.LossDropped, "tool_choice", "Messages has no tools left to force")
	}

	searches := scanWebSearches(req.Items)
	var system []json.RawMessage
	var msgs []*outMessage
	type pendingCacheControl struct {
		turn string
		role string
		path string
		raw  json.RawMessage
	}
	var pendingCC []pendingCacheControl
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
				if raw := e.kept(it.Origin); len(raw) > 0 {
					pendingCC = append(pendingCC, pendingCacheControl{turn: turn, role: it.Role, path: p, raw: raw})
				}
				break
			}
			blocks, paths, sendable, dropped := e.content(it, p)
			if it.Role == llm.RoleUser && dropped && !sendable {
				return nil, nil, invalid("%s: nothing in this user turn can be sent to Messages", p)
			}
			if it.Origin != nil && it.Origin.Format != format {
				if len(blocks) > 0 {
					if raw := e.kept(it.Origin); len(raw) > 0 {
						if rawjson.Object(blocks[len(blocks)-1])["cache_control"] == nil {
							blocks[len(blocks)-1] = rawjson.Merge(blocks[len(blocks)-1], raw)
						} else {
							e.lose(llm.LossDropped, rawjson.Join(p, "cache_control"), "last content block already carries cache_control")
						}
					}
				} else if raw := e.kept(it.Origin); len(raw) > 0 {
					pendingCC = append(pendingCC, pendingCacheControl{turn: turn, role: it.Role, path: p, raw: raw})
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
	if len(pendingCC) > 0 {
		cacheBreakpoints := countCacheBreakpoints(req.Origin, tools, system, msgs)
		for _, cc := range pendingCC {
			var target *outMessage
			for j := len(msgs) - 1; j >= 0; j-- {
				if msgs[j].turn == cc.turn && msgs[j].role == cc.role && len(msgs[j].content) > 0 {
					target = msgs[j]
					break
				}
			}
			if target == nil {
				e.lose(llm.LossDropped, rawjson.Join(cc.path, "cache_control"), "message has no content block to carry cache_control")
				continue
			}
			last := len(target.content) - 1
			if rawjson.Object(target.content[last])["cache_control"] != nil {
				e.lose(llm.LossDropped, rawjson.Join(cc.path, "cache_control"), "last content block already carries cache_control")
				continue
			}
			if cacheBreakpoints >= maxCacheBreakpoints {
				e.lose(llm.LossDropped, rawjson.Join(cc.path, "cache_control"), "Messages allows at most 4 cache breakpoints per request")
				continue
			}
			target.content[last] = rawjson.Merge(target.content[last], cc.raw)
			cacheBreakpoints++
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

func countCacheBreakpoints(origin *llm.Origin, tools, system []json.RawMessage, msgs []*outMessage) int {
	n := 0
	if origin != nil {
		if origin.Format == format {
			if rawjson.Object(origin.Raw)["cache_control"] != nil {
				n++
			}
		} else if m := rawjson.Object(origin.Raw); m != nil {
			if _, ok := validCacheControl(m["cache_control"]); ok {
				n++
			}
		}
	}
	for _, t := range tools {
		if rawjson.Object(t)["cache_control"] != nil {
			n++
		}
	}
	for _, b := range system {
		if rawjson.Object(b)["cache_control"] != nil {
			n++
		}
	}
	for _, m := range msgs {
		for _, b := range m.content {
			obj := rawjson.Object(b)
			if obj["cache_control"] != nil {
				n++
			}
			if rawContent := obj["content"]; len(rawContent) > 0 {
				var nested []json.RawMessage
				if json.Unmarshal(rawContent, &nested) == nil {
					for _, nb := range nested {
						if rawjson.Object(nb)["cache_control"] != nil {
							n++
						}
					}
				}
			}
		}
	}
	return n
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

func (e *encoder) content(it llm.Item, msgPath string) (blocks []json.RawMessage, paths []string, sendable, dropped bool) {
	for i, p := range it.Content {
		pp := pathOf(p.Origin, fmt.Sprintf("%s.content[%d]", msgPath, i))
		var block map[string]any
		switch p.Type {
		case llm.PartText:
			if p.Text == "" {
				e.losses = append(e.losses, rawjson.Losses(p.Origin, format)...)
				continue
			}
			block = map[string]any{"type": "text", "text": p.Text}
		case llm.PartImage, llm.PartFile:
			if it.Role == llm.RoleAssistant {
				e.lose(llm.LossDropped, pp, "Messages does not accept "+p.Type+" blocks in assistant messages")
				dropped = true
				continue
			}
			src := source(p)
			if src == nil {
				e.lose(llm.LossDropped, pp, "Messages cannot take this "+p.Type)
				dropped = true
				continue
			}
			typ := "image"
			if p.Type == llm.PartFile {
				typ = "document"
				if mt, _ := src["media_type"].(string); src["type"] == "base64" && strings.HasPrefix(mediainput.Type(mt), "image/") {
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

func (e *encoder) serviceTier(req llm.Request, opts EncodeOptions, out map[string]any) {
	tier := req.Options.ServiceTier
	if tier == "" {
		return
	}
	own := req.SourceFormat() == format
	t := llm.CanonicalServiceTier(tier)
	if t == llm.ServiceTierPriority {
		if opts.FastMode || own {
			out["speed"] = "fast"
		} else {
			e.lose(llm.LossDropped, llm.ServiceTierPath(req), "Messages takes the priority tier only as fast mode, which this upstream is not sent")
		}
		return
	}
	if !opts.ServiceTier && !own {
		e.lose(llm.LossDropped, llm.ServiceTierPath(req), llm.ServiceTierUnsent)
		return
	}
	switch t {
	case llm.ServiceTierAuto:
		out["service_tier"] = "auto"
	case llm.ServiceTierDefault:
		out["service_tier"] = "standard_only"
	default:
		e.lose(llm.LossDropped, llm.ServiceTierPath(req), fmt.Sprintf("Messages has no %q service tier", tier))
	}
}
