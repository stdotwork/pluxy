package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Prompt caching. A Messages client places its own cache_control markers, and its body is sent as
// it wrote it. A Chat Completions client can place cache_control markers too (top-level, on tools,
// on system/developer/user/assistant/tool messages and content parts, or on tool_calls), and a
// Responses client can ask for prompt_cache_retention ("in_memory" -> "5m", "24h" -> "1h"); those
// are carried over to Messages. Otherwise a translated request gets the breakpoints today's core
// gives it: the end of the system prompt (or, without one, the last tool) and the last block of
// the last message that can hold one, with the account's cache_ttl ("5m" default, or "1h"). Each
// breakpoint caches everything before it
// (https://platform.claude.com/docs/en/build-with-claude/prompt-caching); three stay within
// Messages' limit of four.

const (
	cacheTTL5m = "5m"
	cacheTTL1h = "1h"
)

// ephemeral is the default cache marker: Anthropic's five-minute cache.
var (
	ephemeral     = json.RawMessage(`{"type":"ephemeral"}`)
	ephemeralHour = json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)
)

// uncacheable are block types that cannot carry cache_control.
var uncacheable = map[string]bool{"thinking": true, "redacted_thinking": true}

func cacheMarker(ttl string) json.RawMessage {
	if ttl == cacheTTL1h {
		return ephemeralHour
	}
	return ephemeral
}

// addCacheBreakpoints adds default 5-minute cache breakpoints to a translated Messages body that
// has none.
func addCacheBreakpoints(body []byte) ([]byte, error) {
	return addCacheBreakpointsTTL(body, cacheTTL5m)
}

// addCacheBreakpointsTTL adds cache breakpoints with ttl ("5m" or "1h") to a translated Messages
// body that has none: where Messages takes cache_control (top-level, system blocks, tools, message
// content blocks), none is set.
func addCacheBreakpointsTTL(body []byte, ttl string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	if _, ok := m["cache_control"]; ok {
		return body, nil
	}
	var system, tools []map[string]json.RawMessage
	_ = json.Unmarshal(m["system"], &system)
	_ = json.Unmarshal(m["tools"], &tools)
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(m["messages"], &messages); err != nil {
		return body, nil
	}
	// The content of each user or assistant message given as blocks; nil for the rest.
	contents := make([][]map[string]json.RawMessage, len(messages))
	for i, msg := range messages {
		var role string
		_ = json.Unmarshal(msg["role"], &role)
		if role == "user" || role == "assistant" {
			_ = json.Unmarshal(msg["content"], &contents[i])
		}
	}
	marked := func(blocks []map[string]json.RawMessage) bool {
		for _, b := range blocks {
			if _, ok := b["cache_control"]; ok {
				return true
			}
		}
		return false
	}
	if marked(system) || marked(tools) || slices.ContainsFunc(contents, marked) {
		return body, nil
	}

	marker := cacheMarker(ttl)
	set := func(key string, v any) {
		b, _ := json.Marshal(v)
		m[key] = b
	}
	if len(system) > 0 {
		system[len(system)-1]["cache_control"] = marker
		set("system", system)
	} else if len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = marker
		set("tools", tools)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		content := contents[i]
		j := len(content) - 1
		for ; j >= 0; j-- {
			var typ string
			_ = json.Unmarshal(content[j]["type"], &typ)
			if !uncacheable[typ] {
				break
			}
		}
		if j < 0 {
			continue
		}
		content[j]["cache_control"] = marker
		b, _ := json.Marshal(content)
		messages[i]["content"] = b
		set("messages", messages)
		break
	}
	return json.Marshal(m)
}

// applyClientCache carries over a translated client's own cache settings before automatic
// breakpoints are added: Responses prompt_cache_retention / prompt_cache_options.ttl, and Chat
// Completions cache_control markers on the request, tools, messages and parts.
func applyClientCache(body []byte, req llm.Request, losses []llm.Loss, defaultTTL string) ([]byte, []llm.Loss, string, error) {
	if req.Native == nil {
		return body, losses, defaultTTL, nil
	}
	switch req.Native.Format {
	case llm.FormatOpenAIResponses:
		losses, ttl := applyResponsesCache(req, losses, defaultTTL)
		return body, losses, ttl, nil
	case llm.FormatOpenAIChat:
		out, losses, err := applyChatCache(body, req, losses)
		return out, losses, defaultTTL, err
	default:
		return body, losses, defaultTTL, nil
	}
}

func applyResponsesCache(req llm.Request, losses []llm.Loss, defaultTTL string) ([]llm.Loss, string) {
	ttl := defaultTTL
	if req.Origin == nil || req.Origin.Format != llm.FormatOpenAIResponses {
		return losses, ttl
	}
	rawObj := jsonObject(req.Origin.Raw)
	if rawObj == nil {
		return losses, ttl
	}
	hasRetention := false
	if raw, ok := rawObj["prompt_cache_retention"]; ok {
		var ret string
		if json.Unmarshal(raw, &ret) == nil {
			switch strings.ToLower(strings.TrimSpace(ret)) {
			case "in_memory":
				ttl = cacheTTL5m
				hasRetention = true
				losses = dropLossAt(losses, "prompt_cache_retention")
			case "24h":
				ttl = cacheTTL1h
				hasRetention = true
				losses = dropLossAt(losses, "prompt_cache_retention")
				losses = llm.AddLoss(losses, llm.Loss{
					Kind:   llm.LossApproximated,
					Path:   "prompt_cache_retention",
					Detail: "Messages' longest cache TTL is 1h; sent with ttl 1h",
				})
			}
		}
	}
	if raw, ok := rawObj["prompt_cache_options"]; ok {
		if opts := jsonObject(raw); opts != nil {
			if ttlRaw, ok := opts["ttl"]; ok {
				var optTTL string
				if json.Unmarshal(ttlRaw, &optTTL) == nil {
					mapped := ""
					approx := false
					switch strings.ToLower(strings.TrimSpace(optTTL)) {
					case "in_memory", cacheTTL5m:
						mapped = cacheTTL5m
					case cacheTTL1h:
						mapped = cacheTTL1h
					case "24h":
						mapped = cacheTTL1h
						approx = true
					}
					if mapped != "" {
						if !hasRetention {
							ttl = mapped
						}
						if len(opts) == 1 {
							losses = dropLossAt(losses, "prompt_cache_options")
							if approx && !hasRetention {
								losses = llm.AddLoss(losses, llm.Loss{
									Kind:   llm.LossApproximated,
									Path:   "prompt_cache_options",
									Detail: "Messages' longest cache TTL is 1h; sent with ttl 1h",
								})
							}
						}
					}
				}
			}
		}
	}
	return losses, ttl
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
		if !ok || (ttlStr != cacheTTL5m && ttlStr != cacheTTL1h) {
			return nil, false
		}
	}
	return trimmed, true
}

func jsonObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func dropLossAt(losses []llm.Loss, path string) []llm.Loss {
	return slices.DeleteFunc(losses, func(l llm.Loss) bool {
		return l.Kind == llm.LossDropped && l.Path == path
	})
}

type chatPartSpec struct {
	text   string
	cc     json.RawMessage
	ccOK   bool
	ccPath string
	isText bool
}

type chatToolCallSpec struct {
	callID   string
	custom   bool
	cc       json.RawMessage
	ccOK     bool
	path     string
	dropAttr string // ".cache_control" or ".function"
	dropBoth bool
}

type chatToolResultSpec struct {
	callID   string
	output   string
	partCC   json.RawMessage
	partCCOK bool
	msgCC    json.RawMessage
	msgCCOK  bool
	path     string
	turn     *chatTurn
	matched  bool
}

type chatTurn struct {
	path        string
	role        string
	msgCC       json.RawMessage
	msgCCOK     bool
	parts       []chatPartSpec
	toolCalls   []*chatToolCallSpec
	toolResults []*chatToolResultSpec
	blocks      []map[string]json.RawMessage
}

func (t *chatTurn) addBlock(b map[string]json.RawMessage) {
	t.blocks = append(t.blocks, b)
}

func (t *chatTurn) lastBlock() map[string]json.RawMessage {
	if len(t.blocks) == 0 {
		return nil
	}
	return t.blocks[len(t.blocks)-1]
}

func (t *chatTurn) lastCacheableBlock() map[string]json.RawMessage {
	for i := len(t.blocks) - 1; i >= 0; i-- {
		var typ string
		_ = json.Unmarshal(t.blocks[i]["type"], &typ)
		if !uncacheable[typ] {
			return t.blocks[i]
		}
	}
	return nil
}

func (t *chatTurn) applyMessageMarker(losses []llm.Loss, changed *bool) []llm.Loss {
	if !t.msgCCOK || len(t.blocks) == 0 {
		return losses
	}
	last := t.lastCacheableBlock()
	if last == nil {
		return losses
	}
	if _, has := last["cache_control"]; !has {
		last["cache_control"] = t.msgCC
		*changed = true
	}
	return dropLossAt(losses, t.path+".cache_control")
}

func applyChatCache(body []byte, req llm.Request, losses []llm.Loss) ([]byte, []llm.Loss, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, losses, err
	}
	var system, tools, messages []map[string]json.RawMessage
	_ = json.Unmarshal(m["system"], &system)
	_ = json.Unmarshal(m["tools"], &tools)
	_ = json.Unmarshal(m["messages"], &messages)
	contents := make([][]map[string]json.RawMessage, len(messages))
	for i, msg := range messages {
		_ = json.Unmarshal(msg["content"], &contents[i])
	}

	topChanged := false
	toolsChanged := false
	systemChanged := false
	msgChanged := make([]bool, len(messages))

	// 1. Top-level cache_control.
	if req.Origin != nil && req.Origin.Format == llm.FormatOpenAIChat {
		if cc, ok := validCacheControl(jsonObject(req.Origin.Raw)["cache_control"]); ok {
			m["cache_control"] = cc
			topChanged = true
			losses = dropLossAt(losses, "cache_control")
		}
	}

	// 2. Tool cache_control (tools[i].cache_control or tools[i].function.cache_control).
	outToolIdx := 0
	for _, t := range req.Tools {
		if t.Type == llm.ToolNative {
			continue
		}
		if outToolIdx >= len(tools) {
			break
		}
		if t.Origin != nil && t.Origin.Format == llm.FormatOpenAIChat {
			tObj := jsonObject(t.Origin.Raw)
			fnObj := jsonObject(tObj["function"])
			topCC, topOK := validCacheControl(tObj["cache_control"])
			fnCC, fnOK := validCacheControl(fnObj["cache_control"])
			if topOK {
				tools[outToolIdx]["cache_control"] = topCC
				toolsChanged = true
				losses = dropLossAt(losses, t.Origin.Path+".cache_control")
				if fnOK && len(fnObj) == 1 {
					losses = dropLossAt(losses, t.Origin.Path+".function")
				}
			} else if fnOK {
				tools[outToolIdx]["cache_control"] = fnCC
				toolsChanged = true
				if len(fnObj) == 1 {
					losses = dropLossAt(losses, t.Origin.Path+".function")
				}
			}
		}
		outToolIdx++
	}

	// Parse raw Chat messages so role:"tool" content part cache_control can be lifted onto tool_result.
	var rawChat struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(req.Native.Body, &rawChat)

	// 3. Collect Chat message turns from req.Items in exact source order.
	turns := collectChatTurns(req.Items, rawChat.Messages)

	// 4. Match leading system turns (and all system turns for non-Claude models) against m["system"].
	sysIdx := 0
	var convTurns []*chatTurn
	for _, turn := range turns {
		if (turn.role == llm.RoleSystem || turn.role == llm.RoleDeveloper) && sysIdx < len(system) {
			for _, p := range turn.parts {
				if sysIdx < len(system) && matchBlockPart(system[sysIdx], p) {
					b := system[sysIdx]
					turn.addBlock(b)
					if p.ccOK {
						b["cache_control"] = p.cc
						systemChanged = true
						losses = dropLossAt(losses, p.ccPath)
					}
					sysIdx++
					continue
				}
				if p.ccOK {
					if prev := turn.lastBlock(); prev != nil {
						if _, has := prev["cache_control"]; !has {
							prev["cache_control"] = p.cc
							systemChanged = true
						}
						losses = dropLossAt(losses, p.ccPath)
					}
				}
			}
			losses = turn.applyMessageMarker(losses, &systemChanged)
			continue
		}
		convTurns = append(convTurns, turn)
	}

	// 5. Match tool_use and tool_result blocks unambiguously by CallID.
	tcByID := map[string]*chatToolCallSpec{}
	tcTurn := map[string]*chatTurn{}
	trByID := map[string]*chatToolResultSpec{}
	for _, turn := range convTurns {
		for _, tc := range turn.toolCalls {
			if !tc.custom && tc.callID != "" {
				tcByID[tc.callID] = tc
				tcTurn[tc.callID] = turn
			}
		}
		for _, tr := range turn.toolResults {
			if tr.callID != "" {
				trByID[tr.callID] = tr
			}
		}
	}

	// First pass on contents: collect non-tool blocks per outMsg alongside assistant/system/user turns.
	sysConvTurns := []*chatTurn{}
	otherConvTurns := []*chatTurn{}
	for _, turn := range convTurns {
		if turn.role == llm.RoleSystem || turn.role == llm.RoleDeveloper {
			sysConvTurns = append(sysConvTurns, turn)
		} else if turn.role == llm.RoleAssistant || turn.role == llm.RoleUser {
			otherConvTurns = append(otherConvTurns, turn)
		}
	}

	sysTurnPos := 0
	otherTurnPos := 0

	// Helper to advance user/assistant turns matching non-tool blocks.
	for mi, msg := range messages {
		var role string
		_ = json.Unmarshal(msg["role"], &role)
		blocks := contents[mi]

		switch role {
		case "assistant":
			// Pick the next assistant turn that actually produced blocks in messages[mi].
			for otherTurnPos < len(otherConvTurns) && (otherConvTurns[otherTurnPos].role != llm.RoleAssistant || !turnHasAssistantOutput(otherConvTurns[otherTurnPos])) {
				otherTurnPos++
			}
			var turn *chatTurn
			if otherTurnPos < len(otherConvTurns) {
				turn = otherConvTurns[otherTurnPos]
				otherTurnPos++
			}
			var nonTool []map[string]json.RawMessage
			var toolBlocks []map[string]json.RawMessage
			for _, b := range blocks {
				if blockTypeString(b) == "tool_use" {
					toolBlocks = append(toolBlocks, b)
				} else {
					nonTool = append(nonTool, b)
				}
			}
			if turn != nil {
				losses = matchTurnParts(turn, nonTool, losses, &msgChanged[mi])
			}
			for _, b := range toolBlocks {
				var id string
				_ = json.Unmarshal(b["id"], &id)
				if tc, ok := tcByID[id]; ok {
					t := tcTurn[id]
					t.addBlock(b)
					if tc.ccOK {
						b["cache_control"] = tc.cc
						msgChanged[mi] = true
						losses = dropToolCallLoss(losses, tc)
					}
				}
			}
			if turn != nil {
				losses = turn.applyMessageMarker(losses, &msgChanged[mi])
			}

		case "system":
			// One or more consecutive mid-system turns placed into a system message.
			bIdx := 0
			for bIdx < len(blocks) && sysTurnPos < len(sysConvTurns) {
				turn := sysConvTurns[sysTurnPos]
				sysTurnPos++
				for _, p := range turn.parts {
					if bIdx < len(blocks) && matchBlockPart(blocks[bIdx], p) {
						b := blocks[bIdx]
						turn.addBlock(b)
						if p.ccOK {
							b["cache_control"] = p.cc
							msgChanged[mi] = true
							losses = dropLossAt(losses, p.ccPath)
						}
						bIdx++
						continue
					}
					if p.ccOK {
						if prev := turn.lastBlock(); prev != nil {
							if _, has := prev["cache_control"]; !has {
								prev["cache_control"] = p.cc
								msgChanged[mi] = true
							}
							losses = dropLossAt(losses, p.ccPath)
						}
					}
				}
				losses = turn.applyMessageMarker(losses, &msgChanged[mi])
			}

		case llm.RoleUser:
			// First, attach cache_control to matched tool_result blocks.
			var nonResult []map[string]json.RawMessage
			for _, b := range blocks {
				if blockTypeString(b) == "tool_result" {
					var id string
					_ = json.Unmarshal(b["tool_use_id"], &id)
					if tr, ok := trByID[id]; ok {
						tr.matched = true
						tr.turn.addBlock(b)
						cc, ok := selectToolResultCC(tr)
						if ok {
							b["cache_control"] = cc
							msgChanged[mi] = true
						}
						if tr.msgCCOK {
							losses = dropLossAt(losses, tr.path+".cache_control")
						}
					}
					continue
				}
				nonResult = append(nonResult, b)
			}

			// Split off trailing <system-reminder> blocks produced by placeSystem (takesMidSystem=false)
			// when they match pending mid-system turns.
			reminderStart := len(nonResult)
			if sysTurnPos < len(sysConvTurns) {
				// Count how many trailing blocks look like <system-reminder> or mid-system media.
				// Mid-system turns only move past user turns that precede them, so check backward/forward.
				remIdx := findReminderSplit(nonResult, sysConvTurns[sysTurnPos:])
				reminderStart = remIdx
			}

			userBlocks := nonResult[:reminderStart]
			remBlocks := nonResult[reminderStart:]

			// Match userBlocks sequentially against user turns in otherConvTurns (and any
			// orphaned tool_result converted to text by repairTurns).
			uIdx := 0
			for uIdx < len(userBlocks) {
				for otherTurnPos < len(otherConvTurns) && otherConvTurns[otherTurnPos].role != llm.RoleUser {
					otherTurnPos++
				}
				if otherTurnPos < len(otherConvTurns) {
					turn := otherConvTurns[otherTurnPos]
					if consumed := matchUserTurnParts(turn, userBlocks[uIdx:], &losses, &msgChanged[mi]); consumed > 0 {
						otherTurnPos++
						losses = turn.applyMessageMarker(losses, &msgChanged[mi])
						uIdx += consumed
						continue
					}
				}
				if tr := matchOrphanedToolResult(userBlocks[uIdx], convTurns); tr != nil {
					tr.matched = true
					tr.turn.addBlock(userBlocks[uIdx])
					if cc, ok := selectToolResultCC(tr); ok {
						userBlocks[uIdx]["cache_control"] = cc
						msgChanged[mi] = true
					}
					if tr.msgCCOK {
						losses = dropLossAt(losses, tr.path+".cache_control")
					}
					uIdx++
					continue
				}
				if otherTurnPos < len(otherConvTurns) {
					otherTurnPos++
					continue
				}
				uIdx++
			}

			// Match remBlocks against mid-system turns placed into this user turn.
			rIdx := 0
			for rIdx < len(remBlocks) && sysTurnPos < len(sysConvTurns) {
				turn := sysConvTurns[sysTurnPos]
				sysTurnPos++
				for _, p := range turn.parts {
					if rIdx < len(remBlocks) && matchReminderPart(remBlocks[rIdx], p) {
						b := remBlocks[rIdx]
						turn.addBlock(b)
						if p.ccOK {
							b["cache_control"] = p.cc
							msgChanged[mi] = true
							losses = dropLossAt(losses, p.ccPath)
						}
						rIdx++
						continue
					}
					if p.ccOK {
						if prev := turn.lastBlock(); prev != nil {
							if _, has := prev["cache_control"]; !has {
								prev["cache_control"] = p.cc
								msgChanged[mi] = true
							}
							losses = dropLossAt(losses, p.ccPath)
						}
					}
				}
				losses = turn.applyMessageMarker(losses, &msgChanged[mi])
			}
		}
	}

	if !topChanged && !toolsChanged && !systemChanged && !slices.Contains(msgChanged, true) {
		return body, losses, nil
	}
	if toolsChanged {
		m["tools"], _ = json.Marshal(tools)
	}
	if systemChanged {
		m["system"], _ = json.Marshal(system)
	}
	for i, ch := range msgChanged {
		if ch {
			messages[i]["content"], _ = json.Marshal(contents[i])
		}
	}
	if slices.Contains(msgChanged, true) {
		m["messages"], _ = json.Marshal(messages)
	}
	out, err := json.Marshal(m)
	return out, losses, err
}

func collectChatTurns(items []llm.Item, rawMessages []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}) []*chatTurn {
	var turns []*chatTurn
	byPath := map[string]*chatTurn{}
	turnFor := func(path, role string) *chatTurn {
		if t, ok := byPath[path]; ok {
			return t
		}
		t := &chatTurn{path: path, role: role}
		byPath[path] = t
		turns = append(turns, t)
		return t
	}
	for i, it := range items {
		p := fmt.Sprintf("items[%d]", i)
		if it.Origin != nil && it.Origin.Path != "" {
			p, _, _ = strings.Cut(it.Origin.Path, ".")
		}
		switch it.Type {
		case llm.ItemTypeMessage:
			t := turnFor(p, it.Role)
			if it.Origin != nil && it.Origin.Format == llm.FormatOpenAIChat {
				if cc, ok := validCacheControl(jsonObject(it.Origin.Raw)["cache_control"]); ok {
					t.msgCC = cc
					t.msgCCOK = true
				}
			}
			for k, part := range it.Content {
				ps := chatPartSpec{isText: part.Type == llm.PartText, text: part.Text}
				if part.Origin != nil && part.Origin.Format == llm.FormatOpenAIChat {
					ps.ccPath = part.Origin.Path + ".cache_control"
					if cc, ok := validCacheControl(jsonObject(part.Origin.Raw)["cache_control"]); ok {
						ps.cc = cc
						ps.ccOK = true
					}
				} else if part.Origin != nil {
					ps.ccPath = fmt.Sprintf("%s.content[%d].cache_control", p, k)
				}
				t.parts = append(t.parts, ps)
			}
		case llm.ItemTypeToolCall:
			t := turnFor(p, llm.RoleAssistant)
			tc := &chatToolCallSpec{callID: it.CallID, custom: it.Custom}
			if it.Origin != nil {
				tc.path = it.Origin.Path
				if it.Origin.Format == llm.FormatOpenAIChat {
					tObj := jsonObject(it.Origin.Raw)
					fnObj := jsonObject(tObj["function"])
					topCC, topOK := validCacheControl(tObj["cache_control"])
					fnCC, fnOK := validCacheControl(fnObj["cache_control"])
					if topOK {
						tc.cc, tc.ccOK = topCC, true
						tc.dropAttr = ".cache_control"
						if fnOK && len(fnObj) == 1 {
							tc.dropBoth = true
						}
					} else if fnOK && len(fnObj) == 1 {
						tc.cc, tc.ccOK = fnCC, true
						tc.dropAttr = ".function"
					} else if fnOK {
						tc.cc, tc.ccOK = fnCC, true
					}
				}
			}
			t.toolCalls = append(t.toolCalls, tc)
		case llm.ItemTypeToolResult:
			t := turnFor(p, "tool")
			tr := &chatToolResultSpec{callID: it.CallID, output: it.Output, path: p, turn: t}
			if it.Origin != nil && it.Origin.Format == llm.FormatOpenAIChat {
				if cc, ok := validCacheControl(jsonObject(it.Origin.Raw)["cache_control"]); ok {
					tr.msgCC, tr.msgCCOK = cc, true
				}
			}
			if idx, ok := messageIndex(p); ok && idx < len(rawMessages) {
				var parts []map[string]json.RawMessage
				if json.Unmarshal(rawMessages[idx].Content, &parts) == nil {
					for _, part := range parts {
						if cc, ok := validCacheControl(part["cache_control"]); ok {
							tr.partCC, tr.partCCOK = cc, true
						}
					}
				}
			}
			t.toolResults = append(t.toolResults, tr)
		}
	}
	return turns
}

func messageIndex(path string) (int, bool) {
	rest, ok := strings.CutPrefix(path, "messages[")
	if !ok {
		return 0, false
	}
	num, _, ok := strings.Cut(rest, "]")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil && n >= 0
}

func selectToolResultCC(tr *chatToolResultSpec) (json.RawMessage, bool) {
	if tr.partCCOK {
		return tr.partCC, true
	}
	if tr.msgCCOK {
		return tr.msgCC, true
	}
	return nil, false
}

func dropToolCallLoss(losses []llm.Loss, tc *chatToolCallSpec) []llm.Loss {
	if tc.dropAttr != "" && tc.path != "" {
		losses = dropLossAt(losses, tc.path+tc.dropAttr)
	}
	if tc.dropBoth && tc.path != "" {
		losses = dropLossAt(losses, tc.path+".function")
	}
	return losses
}

func blockTypeString(b map[string]json.RawMessage) string {
	var typ string
	_ = json.Unmarshal(b["type"], &typ)
	return typ
}

func blockTextString(b map[string]json.RawMessage) string {
	var s string
	_ = json.Unmarshal(b["text"], &s)
	return s
}

func matchBlockPart(b map[string]json.RawMessage, p chatPartSpec) bool {
	typ := blockTypeString(b)
	if p.isText {
		if p.text == "" {
			return false
		}
		return typ == "text" && blockTextString(b) == p.text
	}
	return typ == "image" || typ == "document"
}

func matchReminderPart(b map[string]json.RawMessage, p chatPartSpec) bool {
	typ := blockTypeString(b)
	if p.isText {
		if p.text == "" {
			return false
		}
		return typ == "text" && blockTextString(b) == "<system-reminder>\n"+p.text+"\n</system-reminder>"
	}
	return typ == "image" || typ == "document"
}

func matchTurnParts(turn *chatTurn, blocks []map[string]json.RawMessage, losses []llm.Loss, changed *bool) []llm.Loss {
	bi := 0
	for _, p := range turn.parts {
		if bi < len(blocks) && matchBlockPart(blocks[bi], p) {
			b := blocks[bi]
			turn.addBlock(b)
			if p.ccOK {
				b["cache_control"] = p.cc
				*changed = true
				losses = dropLossAt(losses, p.ccPath)
			}
			bi++
			continue
		}
		if p.ccOK {
			if prev := turn.lastBlock(); prev != nil {
				if _, has := prev["cache_control"]; !has {
					prev["cache_control"] = p.cc
					*changed = true
				}
				losses = dropLossAt(losses, p.ccPath)
			}
		}
	}
	return losses
}

func matchUserTurnParts(turn *chatTurn, blocks []map[string]json.RawMessage, losses *[]llm.Loss, changed *bool) int {
	bi := 0
	for _, p := range turn.parts {
		if p.isText && strings.TrimSpace(p.text) == "" {
			// Blank text in a user turn is skipped/dropped by EncodeRequest/RepairTurns.
			if p.ccOK {
				if prev := turn.lastBlock(); prev != nil {
					if _, has := prev["cache_control"]; !has {
						prev["cache_control"] = p.cc
						*changed = true
					}
					*losses = dropLossAt(*losses, p.ccPath)
				}
			}
			continue
		}
		if bi < len(blocks) && matchBlockPart(blocks[bi], p) {
			b := blocks[bi]
			turn.addBlock(b)
			if p.ccOK {
				b["cache_control"] = p.cc
				*changed = true
				*losses = dropLossAt(*losses, p.ccPath)
			}
			bi++
		}
	}
	if len(turn.blocks) == 0 && len(blocks) > 0 && blockTypeString(blocks[0]) == "text" && blockTextString(blocks[0]) == "(empty message)" {
		// All-blank user turn replaced by placeholder; carry hoisted or message marker there.
		b := blocks[0]
		turn.addBlock(b)
		for _, p := range turn.parts {
			if p.ccOK {
				b["cache_control"] = p.cc
				*changed = true
				*losses = dropLossAt(*losses, p.ccPath)
			}
		}
		bi = 1
	}
	return bi
}

func findReminderSplit(blocks []map[string]json.RawMessage, sysTurns []*chatTurn) int {
	if len(blocks) == 0 || len(sysTurns) == 0 {
		return len(blocks)
	}
	// Walk backward from the end of blocks checking whether the trailing suffix matches a prefix
	// of pending mid-system turns converted to <system-reminder> blocks.
	for split := len(blocks) - 1; split >= 0; split-- {
		bi := split
		matchedAny := false
		matchedAll := true
		for _, st := range sysTurns {
			if bi >= len(blocks) {
				break
			}
			for _, p := range st.parts {
				if p.isText && p.text == "" {
					continue
				}
				if bi >= len(blocks) || !matchReminderPart(blocks[bi], p) {
					matchedAll = false
					break
				}
				matchedAny = true
				bi++
			}
			if !matchedAll {
				break
			}
		}
		if matchedAll && matchedAny && bi == len(blocks) {
			return split
		}
	}
	return len(blocks)
}

func turnHasAssistantOutput(t *chatTurn) bool {
	for _, p := range t.parts {
		if !p.isText || p.text != "" {
			return true
		}
	}
	for _, tc := range t.toolCalls {
		if !tc.custom {
			return true
		}
	}
	return false
}

func matchOrphanedToolResult(b map[string]json.RawMessage, turns []*chatTurn) *chatToolResultSpec {
	if blockTypeString(b) != "text" {
		return nil
	}
	text := blockTextString(b)
	for _, turn := range turns {
		for _, tr := range turn.toolResults {
			if tr.matched {
				continue
			}
			want := tr.output
			if strings.TrimSpace(want) == "" {
				want = "(empty tool result)"
			}
			if want == text {
				return tr
			}
		}
	}
	return nil
}
