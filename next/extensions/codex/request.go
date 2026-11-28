package main

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

type upstreamRequest struct {
	body               []byte
	tr                 *llm.Translation
	model              string
	sessionID          string
	names              map[string]string
	keepOutput         bool
	encryptedReasoning []string
}

func (u *upstreamRequest) repairs() *repairs {
	p := newRepairs()
	p.keepOutput = u.keepOutput
	return p
}

func buildRequest(req llm.Request, accountID string, s settings, a account, ws bool) (*upstreamRequest, error) {
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
		host := takesTier(s, a)
		var narrowed []llm.Loss
		if host {
			r, narrowed = narrowToModel(r)
		}
		enc, losses, err := openairesponses.EncodeRequestWith(r, openairesponses.EncodeOptions{
			ServiceTier:   host,
			Verbosity:     host,
			WebSearch:     host,
			WebSearchBare: isChatGPTHost(s, a),
			// No page documents code_interpreter on the ChatGPT backend, and a tool it refuses
			// would fail a request that works without it.
			CodeExecution: host && !isChatGPTHost(s, a),
		})
		if err != nil {
			return nil, err
		}
		tr.Losses = losses
		for _, l := range narrowed {
			tr.Losses = llm.AddLoss(tr.Losses, l)
		}
		if req.DropPolicy == llm.DropReject && tr.Dropped() {
			paths := tr.DroppedPaths()
			return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
				Message: "OpenAI Responses cannot carry parts of this request: " + strings.Join(paths, ", ")}
		}
		body = enc
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the request body is not a JSON object", Scope: llm.ScopeRequest}
	}
	sh := shaper{m: m, req: req, tr: tr, model: model, effort: effort, s: s, a: a, accountID: accountID, ws: ws}
	nativeLite := sh.passthrough() && sh.lite()
	if err := sh.shape(); err != nil {
		return nil, err
	}
	if req.DropPolicy == llm.DropReject && tr.Dropped() {
		paths := tr.DroppedPaths()
		return nil, &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true, Dropped: paths,
			Message: "the Codex endpoint cannot take parts of this request: " + strings.Join(paths, ", ")}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return &upstreamRequest{body: out, tr: tr, model: model, sessionID: sh.sessionID, names: sh.names,
		keepOutput: nativeLite && (ws || req.Stream), encryptedReasoning: sh.encryptedReasoning}, nil
}

func takesTier(s settings, a account) bool {
	u, err := url.Parse(baseURL(s, a))
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "api.openai.com" || h == "chatgpt.com"
}

// isChatGPTHost reports whether the account's upstream is the ChatGPT backend (chatgpt.com), where
// translated web search tools are sent bare ({"type":"web_search"}) because filters, user_location
// and search_context_size are not documented there.
func isChatGPTHost(s settings, a account) bool {
	u, err := url.Parse(baseURL(s, a))
	return err == nil && strings.EqualFold(u.Hostname(), "chatgpt.com")
}

// Fields the Codex endpoint does not take, removed from every request. Codex's own request has no
// metadata field. prompt_cache_retention stays when the account passes it on (see shape).
var removedFields = []string{
	"temperature", "top_p", "max_tokens", "max_completion_tokens", "truncation", "user", "metadata",
	"prompt_cache_retention", "prompt_cache_options", "previous_response_id", "safety_identifier",
	"generate",
}

type shaper struct {
	m         map[string]json.RawMessage
	req       llm.Request
	tr        *llm.Translation
	model     string
	effort    string
	s         settings
	a         account
	accountID string
	ws        bool

	sessionID          string
	names              map[string]string
	encryptedReasoning []string
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
	h.history()
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
	h.minOutputTokens()
	for _, k := range removedFields {
		if h.ws && slices.Contains(wsFields, k) {
			continue
		}
		if k == "prompt_cache_retention" && h.s.PromptCacheRetentionPassthrough {
			continue
		}
		delete(m, k)
	}
	h.streamOptions()
	h.serviceTier()
	if v, ok := m["instructions"]; (!ok || string(v) == "null") && !(h.passthrough() && h.lite()) {
		m["instructions"] = raw("")
	}
	h.reasoning()
	webSearchForHistory(h.m, h.lite())
	h.tools()
	h.limits()
	h.promptCacheKey()
	if h.a.siwc {
		siwcBody(m, h.effort != "")
		if h.ws {
			delete(m, "stream")
			delete(m, "background")
		}
	}
	h.clampEffort()
	return nil
}

// codexMinOutputTokens is the smallest integer max_output_tokens the Codex endpoint takes; it
// answers a smaller one with invalid_request_error (Claude Code's model switch asks for 1).
const codexMinOutputTokens = 16

func (h *shaper) minOutputTokens() {
	v, ok := h.m["max_output_tokens"]
	if !ok {
		return
	}
	n, err := strconv.ParseFloat(string(v), 64)
	if err != nil || n < 1 || n >= codexMinOutputTokens || n != float64(int64(n)) {
		return
	}
	h.m["max_output_tokens"] = raw(codexMinOutputTokens)
}

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

func clampEffortTo(effort string, levels []string) (string, bool) {
	rank := slices.Index(effortOrder, strings.ToLower(effort))
	if rank < 0 {
		return effort, false
	}
	best, bestRank, above, aboveRank := "", -1, "", len(effortOrder)
	for _, l := range levels {
		r := slices.Index(effortOrder, strings.ToLower(l))
		switch {
		case r < 0:
		case r == rank:
			return effort, false
		case r < rank && r > bestRank:
			best, bestRank = l, r
		case r > rank && r < aboveRank:
			above, aboveRank = l, r
		}
	}
	switch {
	case best != "":
		return best, true
	case above != "":
		return above, true
	}
	return effort, false
}

func modelEffortLevels(model string) []string {
	templates := clientTemplates()
	t, ok := templates[model]
	if !ok {
		t, ok = templates[strings.ToLower(model)]
	}
	if !ok {
		return nil
	}
	var levels []struct {
		Effort string `json:"effort"`
	}
	if json.Unmarshal(t["supported_reasoning_levels"], &levels) != nil {
		return nil
	}
	out := make([]string, 0, len(levels))
	for _, l := range levels {
		out = append(out, l.Effort)
	}
	return out
}

func (h *shaper) clampEffort() {
	var r map[string]json.RawMessage
	if json.Unmarshal(h.m["reasoning"], &r) != nil || r == nil {
		return
	}
	var effort string
	if json.Unmarshal(r["effort"], &effort) != nil || effort == "" {
		return
	}
	levels := modelEffortLevels(h.model)
	if len(levels) == 0 {
		return
	}
	to, changed := clampEffortTo(effort, levels)
	if !changed {
		return
	}
	r["effort"] = raw(to)
	h.m["reasoning"] = raw(r)
	if h.tr != nil {
		h.tr.Losses = llm.AddLoss(h.tr.Losses, llm.Loss{Kind: llm.LossApproximated, Path: "reasoning.effort",
			Detail: effort + " is not a reasoning level of " + h.model + "; sent " + to})
	}
}

func (h *shaper) lite() bool {
	var headers map[string]string
	if h.req.Native != nil {
		headers = h.req.Native.Headers
	}
	return liteRequest(headers, h.m)
}

func liteRequest(headers map[string]string, m map[string]json.RawMessage) bool {
	return openairesponses.LiteRequest(headers, m)
}

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
	m["tools"] = raw(append(tools, raw(map[string]string{"type": "web_search"})))
}

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

func (h *shaper) include() {
	inc := []string{"reasoning.encrypted_content"}
	var asked []string
	_ = json.Unmarshal(h.m["include"], &asked)
	if slices.Contains(asked, "web_search_call.action.sources") {
		inc = append(inc, "web_search_call.action.sources")
	}
	if !h.passthrough() && !isChatGPTHost(h.s, h.a) && slices.Contains(asked, "code_interpreter_call.outputs") {
		inc = append(inc, "code_interpreter_call.outputs")
	}
	h.m["include"] = raw(inc)
}

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

func (h *shaper) serviceTier() {
	switch t := h.str("service_tier"); t {
	case "fast", "priority":
		h.m["service_tier"] = raw("priority")
	case "ultrafast":
	default:
		delete(h.m, "service_tier")
	}
}

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

func (h *shaper) tools() {
	var tools []json.RawMessage
	_ = json.Unmarshal(h.m["tools"], &tools)
	from := ""
	if h.req.Native != nil {
		from = h.req.Native.Format
	}
	if _, ok := h.m["tool_choice"]; !ok && len(tools) > 0 && (from == llm.FormatAnthropicMessages || from == llm.FormatGeminiGenerate) {
		h.m["tool_choice"] = raw("auto")
	}
	lite := h.lite()
	if h.s.imageTool() && !h.s.imageOff(h.model) && !lite && !h.a.siwc && !strings.HasSuffix(h.model, "spark") && !strings.EqualFold(h.a.planType, "free") && !hasTool(tools, "image_generation") {
		tools = append(tools, raw(map[string]string{"type": "image_generation", "output_format": "png"}))
		h.m["tools"] = raw(tools)
	}
	lose := func(path, detail string) {
		h.tr.Losses = llm.AddLoss(h.tr.Losses, llm.Loss{Kind: llm.LossApproximated, Path: path, Detail: detail})
	}
	if fixToolSchemas(tools, "tools", lose) {
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

func (h *shaper) header(name string) string {
	if h.req.Native == nil {
		return ""
	}
	return h.req.Native.Headers[name]
}

func (h *shaper) promptCacheKey() {
	if k := h.str("prompt_cache_key"); k != "" {
		h.sessionID = k
		return
	}
	delete(h.m, "prompt_cache_key")
	if h.header("session-id") != "" {
		return
	}
	if session := h.claudeCodeSession(); session != "" {
		agent := strings.TrimSpace(h.header("x-claude-code-agent-id"))
		if agent == "" {
			agent = "main"
		}
		h.sessionID = uuid5("pluxy:codex:claude-code\x00" + h.accountID + "\x00" + h.model + "\x00" + session + "\x00" + agent)
		h.m["prompt_cache_key"] = raw(h.sessionID)
		return
	}
	root := conversationRoot(h.m["input"])
	if root == "" {
		return
	}
	h.sessionID = uuid5("pluxy:codex:prompt-cache\x00" + h.accountID + "\x00" + h.model + "\x00" + root)
	h.m["prompt_cache_key"] = raw(h.sessionID)
}

var sessionSuffix = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

func (h *shaper) claudeCodeSession() string {
	if s := strings.TrimSpace(h.header("x-claude-code-session-id")); s != "" {
		return s
	}
	if h.req.Native == nil || h.req.Native.Format != llm.FormatAnthropicMessages {
		return ""
	}
	var b struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(h.req.Native.Body, &b) != nil {
		return ""
	}
	id := b.Metadata.UserID
	if m := sessionSuffix.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if strings.HasPrefix(id, "{") {
		var u struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(id), &u) == nil {
			return strings.TrimSpace(u.SessionID)
		}
	}
	return ""
}

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
