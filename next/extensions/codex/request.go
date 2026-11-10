package main

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// formatNames name client formats in messages.
var formatNames = map[string]string{
	llm.FormatOpenAIChat:        "Chat Completions",
	llm.FormatOpenAIResponses:   "OpenAI Responses",
	llm.FormatAnthropicMessages: "Anthropic Messages",
	llm.FormatGeminiGenerate:    "Gemini GenerateContent",
}

// upstreamRequest is what one generate call sends.
type upstreamRequest struct {
	body []byte
	tr   *llm.Translation
	// model is the model sent upstream, without a thinking suffix.
	model string
	// sessionID is the prompt cache key the request carries, also sent as the Session-Id header.
	sessionID string
	// names maps the function names the request sent upstream in place of the client's to the
	// client's, for the reply.
	names map[string]string
}

// buildRequest returns the Codex request for req: the client's Responses body, or the request
// translated to Responses, in the shape the Codex endpoint takes (shape). ws shapes it for the
// Responses WebSocket, which keeps previous_response_id and generate.
func buildRequest(req llm.Request, accountID string, s settings, a account, ws bool) (*upstreamRequest, error) {
	// Core has taken a thinking suffix off the model name (llm.SplitThinkingSuffix).
	model, effort := req.Model, ""
	if s := req.Options.ThinkingSuffix; s != "" {
		effort = llm.ThinkingEffort(s)
	}
	tr := &llm.Translation{To: llm.FormatOpenAIResponses}
	var body []byte
	if n := req.Native; n != nil {
		tr.From = n.Format
	}
	if n := req.Native; n != nil && n.Format == llm.FormatOpenAIResponses {
		tr.Passthrough = true
		body = n.Body
	} else {
		r := req
		r.Model = model
		enc, losses, err := openairesponses.EncodeRequest(r)
		if err != nil {
			return nil, err
		}
		tr.Losses = losses
		if req.DropPolicy == llm.DropReject && tr.Dropped() {
			var paths []string
			for _, l := range losses {
				if l.Kind == llm.LossDropped {
					paths = append(paths, l.Path)
				}
			}
			return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
				Message: "OpenAI Responses cannot carry parts of this request: " + strings.Join(paths, ", ")}
		}
		body = enc
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	sh := shaper{m: m, req: req, model: model, effort: effort, s: s, a: a, accountID: accountID, ws: ws}
	if err := sh.shape(); err != nil {
		return nil, err
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &upstreamRequest{body: out, tr: tr, model: model, sessionID: sh.sessionID, names: sh.names}, nil
}

// Fields the Codex endpoint does not take, removed from every request.
var removedFields = []string{
	"temperature", "top_p", "max_tokens", "max_completion_tokens", "truncation", "user",
	"prompt_cache_retention", "prompt_cache_options", "previous_response_id", "safety_identifier",
	"generate",
}

// shaper puts a Responses body in the shape the Codex endpoint takes. The rules follow what
// today's core does to a request before sending it to Codex (the corpus records it); a SIWC
// account's request then gets the SIWC rules (siwcBody).
type shaper struct {
	m         map[string]json.RawMessage
	req       llm.Request
	model     string
	effort    string // from a thinking suffix on the model name
	s         settings
	a         account
	accountID string // the account's ID in core
	ws        bool   // for the Responses WebSocket

	sessionID string
	// names maps each function name the request sent upstream in place of the client's to the
	// client's (see limits).
	names map[string]string
}

// wsFields are fields the Responses WebSocket takes that HTTP does not: a turn continues the
// socket's previous response, and generate: false only warms the socket up.
var wsFields = []string{"previous_response_id", "generate"}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func (h *shaper) str(key string) string {
	var s string
	_ = json.Unmarshal(h.m[key], &s)
	return s
}

func (h *shaper) shape() error {
	m := h.m
	m["model"] = raw(h.model)
	if err := h.input(); err != nil {
		return err
	}
	m["stream"] = raw(true)
	m["store"] = raw(false)
	h.include()
	if _, ok := m["max_output_tokens"]; !ok {
		for _, k := range []string{"max_completion_tokens", "max_tokens"} {
			if v, ok := m[k]; ok && string(v) != "null" {
				m["max_output_tokens"] = v
				break
			}
		}
	}
	for _, k := range removedFields {
		if h.ws && slices.Contains(wsFields, k) {
			continue
		}
		delete(m, k)
	}
	h.streamOptions()
	h.serviceTier()
	// A Responses-lite request from a Responses client goes without instructions when it has
	// none, as today's core sends it; any other request gets empty instructions.
	if v, ok := m["instructions"]; (!ok || string(v) == "null") && !(h.passthrough() && h.lite()) {
		m["instructions"] = raw("")
	}
	h.reasoning()
	// Before tools(), which adds the image generation tool after any web search tool.
	webSearchForHistory(h.m, h.lite())
	h.tools()
	h.limits()
	h.promptCacheKey()
	if h.a.siwc {
		siwcBody(m, h.effort != "")
		if h.ws {
			// Today's core sends a SIWC turn over the WebSocket without these.
			delete(m, "stream")
			delete(m, "background")
		}
	}
	return nil
}

// lite reports whether the client is a Responses-lite client (see liteRequest).
func (h *shaper) lite() bool {
	var headers map[string]string
	if h.req.Native != nil {
		headers = h.req.Native.Headers
	}
	return liteRequest(headers, h.m)
}

// liteRequest reports whether a request comes from a Responses-lite client, as today's core reads
// it: the X-Openai-Internal-Codex-Responses-Lite header (lower-case in headers) says true, or the
// body's client_metadata carries the same flag, as a WebSocket client sends it
// (ws_request_header_x_openai_internal_codex_responses_lite, true or "true"). m holds the
// request's top-level fields.
func liteRequest(headers map[string]string, m map[string]json.RawMessage) bool {
	if strings.EqualFold(strings.TrimSpace(headers["x-openai-internal-codex-responses-lite"]), "true") {
		return true
	}
	var meta struct {
		Lite json.RawMessage `json:"ws_request_header_x_openai_internal_codex_responses_lite"`
	}
	if json.Unmarshal(m["client_metadata"], &meta) != nil {
		return false
	}
	var s string
	return string(meta.Lite) == "true" || json.Unmarshal(meta.Lite, &s) == nil && strings.EqualFold(strings.TrimSpace(s), "true")
}

// webSearchTools are the tool types that declare hosted web search.
var webSearchTools = []string{"web_search", "web_search_preview", "web_search_preview_2025_03_11"}

// webSearchForHistory makes a request that replays web searches acceptable to the Codex endpoint,
// which refuses ("response protection is unavailable") a web_search_call in input when the
// request declares no web search tool. A request with such an item and no web search tool in
// tools gets {"type":"web_search"} added to tools; a Responses-lite request, which does not
// declare hosted tools, has the items removed instead. m holds the request's top-level fields.
func webSearchForHistory(m map[string]json.RawMessage, lite bool) {
	itemType := func(raw json.RawMessage) string {
		var o struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &o)
		return strings.TrimSpace(o.Type)
	}
	var items []json.RawMessage
	if json.Unmarshal(m["input"], &items) != nil {
		return
	}
	replayed := false
	for _, it := range items {
		if itemType(it) == "web_search_call" {
			replayed = true
			break
		}
	}
	if !replayed {
		return
	}
	var tools []json.RawMessage
	_ = json.Unmarshal(m["tools"], &tools)
	for _, t := range tools {
		if slices.Contains(webSearchTools, itemType(t)) {
			return
		}
	}
	if lite {
		kept := make([]json.RawMessage, 0, len(items))
		for _, it := range items {
			if itemType(it) != "web_search_call" {
				kept = append(kept, it)
			}
		}
		m["input"] = raw(kept)
		return
	}
	// tools that is missing or not a list is replaced.
	m["tools"] = raw(append(tools, raw(map[string]string{"type": "web_search"})))
}

// input makes input a list of items (a string is one user message), gives system messages the
// developer role, and gives function calls with blank arguments "{}".
func (h *shaper) input() error {
	v, ok := h.m["input"]
	if !ok || string(v) == "null" {
		h.m["input"] = raw([]any{})
		return nil
	}
	var text string
	if json.Unmarshal(v, &text) == nil {
		h.m["input"] = raw([]any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]string{"type": "input_text", "text": text}}}})
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(v, &items); err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "input must be a string or a list of items", Scope: llm.ScopeRequest}
	}
	for i, it := range items {
		var o map[string]json.RawMessage
		if json.Unmarshal(it, &o) != nil || o == nil {
			continue
		}
		var typ, role, args string
		_ = json.Unmarshal(o["type"], &typ)
		_ = json.Unmarshal(o["role"], &role)
		changed := false
		if role == "system" && (typ == "" || typ == "message") {
			o["role"] = raw("developer")
			changed = true
		}
		if typ == "function_call" {
			if a, ok := o["arguments"]; !ok || string(a) == "null" || (json.Unmarshal(a, &args) == nil && strings.TrimSpace(args) == "") {
				o["arguments"] = raw("{}")
				changed = true
			}
		}
		if changed {
			items[i] = raw(o)
		}
	}
	h.m["input"] = raw(items)
	return nil
}

// include asks for the reasoning's encrypted content, which the Codex endpoint needs to carry
// reasoning between turns without storing responses. Of the client's include values only web
// search sources are kept.
func (h *shaper) include() {
	inc := []string{"reasoning.encrypted_content"}
	var asked []string
	_ = json.Unmarshal(h.m["include"], &asked)
	if slices.Contains(asked, "web_search_call.action.sources") {
		inc = append(inc, "web_search_call.action.sources")
	}
	h.m["include"] = raw(inc)
}

// streamOptions keeps only reasoning_summary_delivery of the client's stream_options.
func (h *shaper) streamOptions() {
	var so map[string]json.RawMessage
	if json.Unmarshal(h.m["stream_options"], &so) == nil {
		if v, ok := so["reasoning_summary_delivery"]; ok {
			h.m["stream_options"] = raw(map[string]json.RawMessage{"reasoning_summary_delivery": v})
			return
		}
	}
	delete(h.m, "stream_options")
}

// serviceTier keeps the service tiers the Codex endpoint takes: fast and priority become
// priority, ultrafast stays, anything else is removed.
func (h *shaper) serviceTier() {
	switch t := h.str("service_tier"); t {
	case "fast", "priority":
		h.m["service_tier"] = raw("priority")
	case "ultrafast":
	default:
		delete(h.m, "service_tier")
	}
}

// reasoning sets the request's reasoning settings. A thinking suffix's effort wins. A translated
// request (Chat, Messages or Gemini) that names no effort gets medium, or for Messages its
// output_config effort, as today's core sends it; a Responses request is left as the client sent
// it. A Chat client that names an effort also asks for a reasoning summary, which comes back as
// reasoning_content.
func (h *shaper) reasoning() {
	var r map[string]json.RawMessage
	_ = json.Unmarshal(h.m["reasoning"], &r)
	_, named := r["effort"]
	from := ""
	if h.req.Native != nil {
		from = h.req.Native.Format
	}
	if from == llm.FormatOpenAIChat && named {
		if _, ok := r["summary"]; !ok {
			r["summary"] = raw("auto")
		}
	}
	switch {
	case h.effort != "":
		if r == nil {
			r = map[string]json.RawMessage{}
		}
		r["effort"] = raw(h.effort)
	case !named && from != "" && from != llm.FormatOpenAIResponses:
		effort := "medium"
		if from == llm.FormatAnthropicMessages {
			var body struct {
				OutputConfig struct {
					Effort string `json:"effort"`
				} `json:"output_config"`
			}
			_ = json.Unmarshal(h.req.Native.Body, &body)
			if e := strings.ToLower(strings.TrimSpace(body.OutputConfig.Effort)); e != "" {
				effort = e
			}
		}
		if r == nil {
			r = map[string]json.RawMessage{}
		}
		r["effort"] = raw(effort)
	}
	if r != nil {
		h.m["reasoning"] = raw(r)
	}
}

// tools offers the hosted image_generation tool, as the Codex CLI does, and sets
// parallel_tool_calls: on when there are tools and the client did not turn it off, absent when
// there are none. A Responses-lite client (the X-Openai-Internal-Codex-Responses-Lite header)
// gets neither image generation nor parallel calls. It also rewrites the schema forms of function
// tools that the endpoint rejects (fixToolSchemas).
func (h *shaper) tools() {
	var tools []json.RawMessage
	_ = json.Unmarshal(h.m["tools"], &tools)
	from := ""
	if h.req.Native != nil {
		from = h.req.Native.Format
	}
	if _, ok := h.m["tool_choice"]; !ok && len(tools) > 0 && (from == llm.FormatAnthropicMessages || from == llm.FormatGeminiGenerate) {
		// Messages and Gemini requests leave the choice to the model when they name none.
		h.m["tool_choice"] = raw("auto")
	}
	lite := h.lite()
	// A SIWC account gets no image tool, as in today's core.
	if h.s.imageTool() && !lite && !h.a.siwc && !strings.HasSuffix(h.model, "spark") && !strings.EqualFold(h.a.planType, "free") && !hasTool(tools, "image_generation") {
		tools = append(tools, raw(map[string]string{"type": "image_generation", "output_format": "png"}))
		h.m["tools"] = raw(tools)
	}
	// The endpoint rejects some schema forms whoever wrote the tool, so this covers the client's
	// own Responses tools too.
	if fixToolSchemas(tools) {
		h.m["tools"] = raw(tools)
	}
	for i, t := range tools {
		var o map[string]json.RawMessage
		if json.Unmarshal(t, &o) != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(o["type"], &typ)
		if typ != "function" || h.passthrough() {
			continue
		}
		if _, ok := o["strict"]; !ok {
			// Translated function tools say strict false: the client's schema is taken as it
			// is, not checked against strict mode's rules.
			o["strict"] = raw(false)
		}
		if from == llm.FormatGeminiGenerate {
			o["parameters"] = closedSchema(o["parameters"])
		}
		tools[i] = raw(o)
		h.m["tools"] = raw(tools)
	}
	switch {
	case lite:
		h.m["parallel_tool_calls"] = raw(false)
	case len(tools) == 0:
		delete(h.m, "parallel_tool_calls")
	case string(h.m["parallel_tool_calls"]) != "false":
		h.m["parallel_tool_calls"] = raw(true)
	}
}

// closedSchema returns a Gemini function's parameter schema as today's core sends it to Codex:
// without $schema, and with additionalProperties false at the top.
func closedSchema(params json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil || m == nil {
		return params
	}
	delete(m, "$schema")
	m["additionalProperties"] = raw(false)
	return raw(m)
}

func hasTool(tools []json.RawMessage, typ string) bool {
	for _, t := range tools {
		var o struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(t, &o) == nil && o.Type == typ {
			return true
		}
	}
	return false
}

func (h *shaper) passthrough() bool {
	return h.req.Native != nil && h.req.Native.Format == llm.FormatOpenAIResponses
}

// header returns a client header that came with the request (lower-case name).
func (h *shaper) header(name string) string {
	if h.req.Native == nil {
		return ""
	}
	return h.req.Native.Headers[name]
}

// promptCacheKey sets the request's prompt cache key, which keeps a conversation's requests on
// the upstream's warm prompt cache: the client's prompt_cache_key, else (when the client names
// no session of its own with Session-Id) a key derived from the account and the conversation's
// start, so every turn of a conversation gets the same key.
func (h *shaper) promptCacheKey() {
	if k := h.str("prompt_cache_key"); k != "" {
		h.sessionID = k
		return
	}
	delete(h.m, "prompt_cache_key")
	if h.header("session-id") != "" {
		return
	}
	root := conversationRoot(h.m["input"])
	if root == "" {
		return
	}
	h.sessionID = uuid5("pluxy:codex:prompt-cache\x00" + h.accountID + "\x00" + h.model + "\x00" + root)
	h.m["prompt_cache_key"] = raw(h.sessionID)
}

// conversationRoot returns what identifies a conversation from its first turn: the text of the
// developer messages before the first user message, and that message's text.
func conversationRoot(input json.RawMessage) string {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(input, &items) != nil {
		return ""
	}
	var b strings.Builder
	for _, it := range items {
		if it.Role != "developer" && it.Role != "user" {
			continue
		}
		b.WriteString(it.Role)
		b.WriteByte(0)
		b.WriteString(contentText(it.Content))
		b.WriteByte(0)
		if it.Role == "user" {
			return b.String()
		}
	}
	return ""
}

func contentText(c json.RawMessage) string {
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(c, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// uuid5 returns a name-based UUID (RFC 9562 version 5) for name in the OID namespace.
func uuid5(name string) string {
	ns := []byte{0x6b, 0xa7, 0xb8, 0x12, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	h := sha1.New()
	h.Write(ns)
	h.Write([]byte(name))
	u := h.Sum(nil)[:16]
	u[6] = u[6]&0x0f | 0x50
	u[8] = u[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}
