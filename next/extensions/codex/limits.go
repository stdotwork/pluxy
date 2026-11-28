package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	itemIDLimit = 64
	callIDLimit = 64
	nameLimit   = 64
	hashChars   = 16
)

var itemIDPrefixes = map[string]string{
	"message":                 "msg",
	"reasoning":               "rs",
	"function_call":           "fc",
	"custom_tool_call":        "ctc",
	"custom_tool_call_output": "ctco",
	"tool_search_call":        "tsc",
}

func (h *shaper) limits() {
	var raws []json.RawMessage
	if json.Unmarshal(h.m["input"], &raws) != nil {
		return
	}
	objs := make([]map[string]json.RawMessage, len(raws))
	for i, r := range raws {
		_ = json.Unmarshal(r, &objs[i])
	}
	changed := make([]bool, len(raws))
	if !h.passthrough() {
		h.limitNames(objs, changed)
		for i, o := range objs {
			if id, ok := str(o["call_id"]); ok && len(id) > callIDLimit {
				o["call_id"] = raw(shortCallID(id))
				changed[i] = true
			}
		}
	}
	dropped := limitItemIDs(objs, changed)
	out := make([]json.RawMessage, 0, len(raws))
	edited := false
	for i, r := range raws {
		switch {
		case dropped[i]:
			edited = true
		case changed[i]:
			edited = true
			out = append(out, raw(objs[i]))
		default:
			out = append(out, r)
		}
	}
	if edited {
		h.m["input"] = raw(out)
	}
}

func str(v json.RawMessage) (string, bool) {
	var s string
	if len(v) == 0 || json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

type nameHolder struct {
	o    map[string]json.RawMessage
	mark func()
}

func (h *shaper) limitNames(objs []map[string]json.RawMessage, changed []bool) {
	var tools []json.RawMessage
	_ = json.Unmarshal(h.m["tools"], &tools)
	toolObjs := make([]map[string]json.RawMessage, len(tools))
	toolsChanged := false
	var holders []nameHolder
	named := func(o map[string]json.RawMessage) bool {
		t, _ := str(o["type"])
		return t == "function" || t == "custom"
	}
	for i, t := range tools {
		if json.Unmarshal(t, &toolObjs[i]) == nil && named(toolObjs[i]) {
			holders = append(holders, nameHolder{toolObjs[i], func() { toolsChanged = true }})
		}
	}
	var choice map[string]json.RawMessage
	choiceChanged := false
	if json.Unmarshal(h.m["tool_choice"], &choice) == nil && choice != nil && named(choice) {
		holders = append(holders, nameHolder{choice, func() { choiceChanged = true }})
	}
	for i, o := range objs {
		if t, _ := str(o["type"]); t == "function_call" || t == "custom_tool_call" {
			holders = append(holders, nameHolder{o, func() { changed[i] = true }})
		}
	}

	var names []string
	for _, hd := range holders {
		if n, ok := str(hd.o["name"]); ok && n != "" {
			names = append(names, n)
		}
	}
	sent := shortNames(names)
	for _, hd := range holders {
		n, _ := str(hd.o["name"])
		if s := sent[n]; n != "" && s != n {
			hd.o["name"] = raw(s)
			hd.mark()
			if h.names == nil {
				h.names = map[string]string{}
			}
			h.names[s] = n
		}
	}
	if toolsChanged {
		for i, o := range toolObjs {
			if o != nil {
				tools[i] = raw(o)
			}
		}
		h.m["tools"] = raw(tools)
	}
	if choiceChanged {
		h.m["tool_choice"] = raw(choice)
	}
}

func shortNames(names []string) map[string]string {
	used := map[string]bool{}
	m := map[string]string{}
	for _, n := range names {
		if _, done := m[n]; done {
			continue
		}
		s := shortName(n)
		if used[s] {
			base := s
			for i := 1; used[s]; i++ {
				suffix := "_" + strconv.Itoa(i)
				s = base[:min(len(base), nameLimit-len(suffix))] + suffix
			}
		}
		used[s] = true
		m[n] = s
	}
	return m
}

func shortName(name string) string {
	s := portable(name)
	if len(s) <= nameLimit {
		return s
	}
	if strings.HasPrefix(s, "mcp__") {
		if i := strings.LastIndex(s, "__"); i > 0 {
			s = "mcp__" + s[i+2:]
			return s[:min(len(s), nameLimit)]
		}
	}
	return s[:nameLimit]
}

func portable(s string) string {
	ok := true
	for i := 0; i < len(s) && ok; i++ {
		ok = portableByte(s[i])
	}
	if ok {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r < utf8.RuneSelf && portableByte(byte(r)) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func portableByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func shortCallID(id string) string {
	if len(id) <= callIDLimit {
		return id
	}
	suffix := hashSuffix(id, 0)
	n := callIDLimit - len(suffix)
	for n > 0 && !utf8.RuneStart(id[n]) {
		n--
	}
	return id[:n] + suffix
}

func hashSuffix(id string, attempt int) string {
	if attempt > 0 {
		id += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(id))
	return "_" + hex.EncodeToString(sum[:])[:hashChars]
}

func limitItemIDs(objs []map[string]json.RawMessage, changed []bool) (dropped []bool) {
	dropped = make([]bool, len(objs))
	type item struct {
		typ, id, normal string
		ok              bool
		keep            bool
	}
	items := make([]item, len(objs))
	taken := map[string]bool{}
	for i, o := range objs {
		id, ok := str(o["id"])
		if !ok {
			continue
		}
		typ, _ := str(o["type"])
		if unsendableReasoning(o) {
			dropped[i] = true
			continue
		}
		normal := normalItemID(typ, id)
		items[i] = item{typ, id, normal, true, normal == id && utf8.RuneCountInString(id) <= itemIDLimit}
		if items[i].keep {
			taken[id] = true
		}
	}
	final := map[[2]string]string{}
	for i, it := range items {
		if !it.ok || it.keep {
			continue
		}
		key := [2]string{it.normal, it.id}
		id, done := final[key]
		if !done {
			id = it.normal
			if utf8.RuneCountInString(id) > itemIDLimit || taken[id] {
				base := prefixedItemID(it.typ, it.id)
				for attempt := 0; ; attempt++ {
					if id = shortItemID(base, attempt); !taken[id] {
						break
					}
				}
			}
			taken[id] = true
			final[key] = id
		}
		objs[i]["id"] = raw(id)
		changed[i] = true
	}
	return dropped
}

func prefixedItemID(typ, id string) string {
	if after, ok := strings.CutPrefix(id, "ts_"); ok && typ == "tool_search_call" && after != "" {
		return "tsc_" + after
	}
	p, ok := itemIDPrefixes[typ]
	if !ok || id == "" || strings.HasPrefix(id, p) {
		return id
	}
	return p + "_" + id
}

func normalItemID(typ, id string) string {
	id = prefixedItemID(typ, id)
	if utf8.RuneCountInString(id) > itemIDLimit {
		return id
	}
	return portable(id)
}

func shortItemID(id string, attempt int) string {
	suffix := hashSuffix(id, attempt)
	runes := []rune(id)
	return portable(string(runes[:min(len(runes), itemIDLimit-len(suffix))])) + suffix
}
