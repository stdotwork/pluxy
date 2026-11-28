package anthropicmessages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	"github.com/stdotwork/pluxy/next/llm"
)

const maxToolName = 64

func validToolName(name string) bool {
	if name == "" || len(name) > maxToolName {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !portable(name[i]) {
			return false
		}
	}
	return true
}

func portable(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func sanitizeToolName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if !portable(c) {
			b[i] = '_'
		}
	}
	if len(b) > maxToolName {
		b = b[:maxToolName]
	}
	return string(b)
}

func ToolNames(req llm.Request) map[string]string {
	req, _ = clienttools.Lower(req)
	n := toolNames{to: map[string]string{}, taken: map[string]bool{}}
	var declared, called []string
	for _, t := range req.Tools {
		if t.IsFunction() {
			declared = append(declared, t.Name)
		}
	}
	for _, it := range req.Items {
		if it.Type == llm.ItemTypeToolCall && !it.Custom {
			called = append(called, it.Name)
		}
	}
	n.allocate(declared)
	n.allocate(called)
	changed := map[string]string{}
	for from, to := range n.to {
		if from != to {
			changed[from] = to
		}
	}
	return changed
}

type toolNames struct {
	to    map[string]string
	taken map[string]bool
}

func (n *toolNames) assign(from, to string) {
	n.to[from] = to
	n.taken[to] = true
}

func (n *toolNames) allocate(names []string) {
	var changed []string
	seen := map[string]bool{}
	for _, name := range names {
		if _, done := n.to[name]; done || name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if validToolName(name) && !n.taken[name] {
			n.assign(name, name)
			continue
		}
		changed = append(changed, name)
	}
	count := map[string]int{}
	for _, name := range changed {
		count[sanitizeToolName(name)]++
	}
	var hashed []string
	for _, name := range changed {
		if base := sanitizeToolName(name); count[base] == 1 && !n.taken[base] {
			n.assign(name, base)
		} else {
			hashed = append(hashed, name)
		}
	}
	slices.Sort(hashed)
	for _, name := range hashed {
		base := sanitizeToolName(name)
		if len(base) > 53 {
			base = base[:53]
		}
		for i := 0; ; i++ {
			seed := name
			if i > 0 {
				seed += "\x00" + strconv.Itoa(i)
			}
			sum := sha256.Sum256([]byte(seed))
			if to := base + "_" + hex.EncodeToString(sum[:])[:10]; !n.taken[to] {
				n.assign(name, to)
				break
			}
		}
	}
}

func toolName(names map[string]string, name string) string {
	if to, ok := names[name]; ok {
		return to
	}
	if name == "" || validToolName(name) {
		return name
	}
	return sanitizeToolName(name)
}

const emptySchema = `{"type":"object","properties":{}}`

func inputSchema(schema json.RawMessage) json.RawMessage {
	var root map[string]json.RawMessage
	if len(schema) == 0 || json.Unmarshal(schema, &root) != nil || root == nil {
		return json.RawMessage(emptySchema)
	}
	properties := schemaObject(root["properties"])
	for _, union := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := root[union]
		if !ok {
			continue
		}
		delete(root, union)
		var branches []json.RawMessage
		if json.Unmarshal(raw, &branches) != nil {
			continue
		}
		for _, br := range branches {
			var branch map[string]json.RawMessage
			if json.Unmarshal(br, &branch) != nil || !canBeObject(branch) {
				continue
			}
			for name, p := range schemaObject(branch["properties"]) {
				if _, ok := properties[name]; !ok {
					properties[name] = p
				}
			}
			if union == "allOf" {
				addRequired(root, branch["required"])
			}
		}
	}
	root["type"] = json.RawMessage(`"object"`)
	p, err := json.Marshal(properties)
	if err != nil {
		return json.RawMessage(emptySchema)
	}
	root["properties"] = p
	out, err := json.Marshal(root)
	if err != nil {
		return json.RawMessage(emptySchema)
	}
	return out
}

func schemaObject(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

func canBeObject(schema map[string]json.RawMessage) bool {
	raw, ok := schema["type"]
	if !ok {
		return true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == "object"
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && slices.Contains(many, "object")
}

func addRequired(root map[string]json.RawMessage, branch json.RawMessage) {
	var names []string
	if json.Unmarshal(branch, &names) != nil {
		return
	}
	var required []string
	if raw, ok := root["required"]; ok && json.Unmarshal(raw, &required) != nil {
		required = nil
	}
	for _, name := range names {
		if !slices.Contains(required, name) {
			required = append(required, name)
		}
	}
	if len(required) == 0 {
		return
	}
	if b, err := json.Marshal(required); err == nil {
		root["required"] = b
	}
}
