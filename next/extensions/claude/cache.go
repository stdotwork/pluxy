package main

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	cacheTTL5m = "5m"
	cacheTTL1h = "1h"
)

var (
	ephemeral     = json.RawMessage(`{"type":"ephemeral"}`)
	ephemeralHour = json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)
)

var uncacheable = map[string]bool{"thinking": true, "redacted_thinking": true}

func cacheMarker(ttl string) json.RawMessage {
	if ttl == cacheTTL1h {
		return ephemeralHour
	}
	return ephemeral
}

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

func applyClientCache(body []byte, req llm.Request, losses []llm.Loss, defaultTTL string) ([]byte, []llm.Loss, string, error) {
	if req.Native == nil || req.Native.Format != llm.FormatOpenAIResponses {
		return body, losses, defaultTTL, nil
	}
	losses, ttl := applyResponsesCache(req, losses, defaultTTL)
	return body, losses, ttl, nil
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
						losses = dropLossAt(losses, "prompt_cache_options")
						if hasRetention {
							if mapped != ttl {
								losses = llm.AddLoss(losses, llm.Loss{
									Kind:   llm.LossDropped,
									Path:   "prompt_cache_options.ttl",
									Detail: "prompt_cache_retention took precedence",
								})
							}
						} else {
							ttl = mapped
							if approx {
								losses = llm.AddLoss(losses, llm.Loss{
									Kind:   llm.LossApproximated,
									Path:   "prompt_cache_options.ttl",
									Detail: "Messages' longest cache TTL is 1h; sent with ttl 1h",
								})
							}
						}
						if len(opts) > 1 {
							keys := make([]string, 0, len(opts)-1)
							for k := range opts {
								if k != "ttl" {
									keys = append(keys, k)
								}
							}
							slices.Sort(keys)
							for _, k := range keys {
								losses = llm.AddLoss(losses, llm.Loss{
									Kind:   llm.LossDropped,
									Path:   "prompt_cache_options." + k,
									Detail: "no place for it in anthropic.messages",
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
