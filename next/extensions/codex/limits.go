package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The Codex endpoint refuses a request whose input item IDs, call IDs or function names are over
// its limits or use characters it does not take. A client, or the conversation of another
// provider that the client replays, can carry such values: a Claude Code MCP tool is named
// mcp__<server>__<tool>, and other providers' item IDs are long or hold + and /. limits rewrites
// them the way today's core does, so the request is not refused.
const (
	itemIDLimit = 64 // input item IDs, in characters
	callIDLimit = 64 // call IDs, in bytes
	nameLimit   = 64 // function names, in bytes
	hashChars   = 16 // hex characters of SHA-256 that end a shortened ID
)

// itemIDPrefixes are the prefixes the endpoint wants on the IDs of these item types.
var itemIDPrefixes = map[string]string{
	"message":                 "msg",
	"reasoning":               "rs",
	"function_call":           "fc",
	"custom_tool_call":        "ctc",
	"custom_tool_call_output": "ctco",
	"tool_search_call":        "tsc",
}

// limits keeps the request's IDs and names within the endpoint's limits. Input item IDs are
// made so on every request. Call IDs and function names are made so only on a request core
// translated, not on a Responses client's own body, which today's core sends as it came. When a
// name changes, h.names records how to give the client its own name back in the reply.
func (h *shaper) limits() {
	var raws []json.RawMessage
	if json.Unmarshal(h.m["input"], &raws) != nil {
		return
	}
	objs := make([]map[string]json.RawMessage, len(raws))
	for i, r := range raws {
		_ = json.Unmarshal(r, &objs[i]) // an item that is not an object stays nil
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

// str returns v's value when v is a JSON string.
func str(v json.RawMessage) (string, bool) {
	var s string
	if len(v) == 0 || json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

// nameHolder is an object that names a function: a function or custom tool, the tool choice, or
// a function or custom tool call in the input.
type nameHolder struct {
	o    map[string]json.RawMessage
	mark func()
}

// limitNames gives every function name the endpoint takes, in the tools, the tool choice and the
// calls in the conversation so far, and fills h.names. Two names that would end up the same are
// told apart (see shortNames). The names are counted in this order, as today's core counts them.
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

// shortNames maps each of names to the name sent in its place (the same name, when it needs no
// change). A name is made of ASCII letters, digits, _ and -, and at most 64 bytes: other
// characters become _, and a longer name is cut to 64 bytes after keeping "mcp__" and what
// follows the last "__" of an MCP tool name (mcp__server__tool becomes mcp__tool). Names that come
// out the same get _1, _2 and so on, so the model can tell its tools apart.
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

// portable replaces every character other than an ASCII letter, digit, _ and - with _.
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

// shortCallID cuts a call ID over the limit to 47 bytes and ends it with _ and 16 hex characters
// of its SHA-256. The same ID always comes out the same, so a call and its result still pair.
func shortCallID(id string) string {
	if len(id) <= callIDLimit {
		return id
	}
	suffix := hashSuffix(id, 0)
	n := callIDLimit - len(suffix)
	for n > 0 && !utf8.RuneStart(id[n]) {
		n-- // do not cut a character in two
	}
	return id[:n] + suffix
}

// hashSuffix returns "_" and 16 hex characters of the SHA-256 of id; a later attempt hashes the
// attempt number in as well, for another suffix of the same ID.
func hashSuffix(id string, attempt int) string {
	if attempt > 0 {
		id += "\x00" + strconv.Itoa(attempt)
	}
	sum := sha256.Sum256([]byte(id))
	return "_" + hex.EncodeToString(sum[:])[:hashChars]
}

// limitItemIDs gives the IDs of the input items (objs; nil for an item that is not an object) the
// endpoint takes, and marks the items it changed. It returns the items to leave out: a reasoning
// item with encrypted content whose ID is over the limit, which could not be sent as it is and
// whose content the endpoint could not read under another ID.
//
// An ID gets the prefix of its item type if it lacks one, then ASCII letters, digits, _ and -
// only. One over 64 characters is cut to 47 and ends with _ and 16 hex characters of the SHA-256
// of the ID. An ID that needs no change is never changed, and two different IDs never end up
// the same: the one that needs a change takes another hash suffix. The same ID with the same
// type always comes out the same, and a result goes through again unchanged.
func limitItemIDs(objs []map[string]json.RawMessage, changed []bool) (dropped []bool) {
	dropped = make([]bool, len(objs))
	type item struct {
		typ, id, normal string
		ok              bool // the item has an ID
		keep            bool // the ID needs no change
	}
	items := make([]item, len(objs))
	taken := map[string]bool{} // IDs the result already uses
	for i, o := range objs {
		id, ok := str(o["id"])
		if !ok {
			continue
		}
		typ, _ := str(o["type"])
		if typ == "reasoning" && utf8.RuneCountInString(id) > itemIDLimit {
			if enc, _ := str(o["encrypted_content"]); enc != "" {
				dropped[i] = true
				continue
			}
		}
		normal := normalItemID(typ, id)
		items[i] = item{typ, id, normal, true, normal == id && utf8.RuneCountInString(id) <= itemIDLimit}
		if items[i].keep {
			taken[id] = true
		}
	}
	final := map[[2]string]string{} // (normal ID, ID) -> the ID sent
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

// prefixedItemID gives id the prefix of its item type, unless it starts with it. A tool search
// call's legacy ts_ prefix becomes tsc_.
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

// normalItemID is the ID with its type's prefix and, when that is within the limit, only the
// characters the endpoint takes. One over the limit is left for shortItemID, which cleans the
// part it keeps.
func normalItemID(typ, id string) string {
	id = prefixedItemID(typ, id)
	if utf8.RuneCountInString(id) > itemIDLimit {
		return id
	}
	return portable(id)
}

// shortItemID returns the first 47 characters of id, cleaned, and a hash suffix.
func shortItemID(id string, attempt int) string {
	suffix := hashSuffix(id, attempt)
	runes := []rune(id)
	return portable(string(runes[:min(len(runes), itemIDLimit-len(suffix))])) + suffix
}
