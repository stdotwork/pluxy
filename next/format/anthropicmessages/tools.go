package anthropicmessages

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/stdotwork/pluxy/next/llm"
)

// Tool names. Messages takes a tool name of 1 to 64 letters, digits, underscores and hyphens; a
// client in another format may use others (MCP tools often have dots or colons). Such a name is
// sent under a Messages name, and the reply's calls go back to the client under its own (the
// Claude provider maps them back with ToolNames). The names are those today's core gives them.

// maxToolName is the longest tool name Messages takes.
const maxToolName = 64

// validToolName reports whether Messages takes name as it is.
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

// portable reports whether c is a letter, digit, underscore or hyphen, the characters Messages
// takes in tool names and tool call IDs.
func portable(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// sanitizeToolName replaces each character Messages does not take with an underscore and cuts
// the name to 64 bytes.
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

// ToolNames returns the Messages name of each function name in req that Messages does not take
// as it is, keyed by the client's name: the tools' names first, then the names of the tool calls
// replayed in its items. Every name sent is distinct. A name is kept when it is valid and not
// taken, otherwise sanitized (each other character an underscore, at most 64 bytes); when two
// sanitized names would be the same, or one is already taken, each gets a hash of the client's
// name after the first 53 bytes. The result depends only on req, so the request and its reply
// agree.
func ToolNames(req llm.Request) map[string]string {
	n := toolNames{to: map[string]string{}, taken: map[string]bool{}}
	var declared, called []string
	for _, t := range req.Tools {
		if t.Type != llm.ToolNative {
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

// toolName returns the Messages name for a client's function name.
func toolName(names map[string]string, name string) string {
	if to, ok := names[name]; ok {
		return to
	}
	if name == "" || validToolName(name) {
		return name
	}
	return sanitizeToolName(name)
}

// emptySchema is the input schema of a tool that takes no arguments.
const emptySchema = `{"type":"object","properties":{}}`

// inputSchema makes a tool's parameter schema one Messages takes: an object schema without anyOf,
// oneOf or allOf at its root. The properties of the root branches that can be objects are merged
// into the root's (a property the root has wins), and the required names of allOf branches are
// added to the root's. A schema that is missing or not a JSON object becomes the empty object
// schema. This is what today's core does to a translated tool's schema. Patterns are left as
// they are: Messages takes Unicode property escapes such as \p{L}.
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

// schemaObject reads a JSON object of subschemas; anything else is an empty one.
func schemaObject(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// canBeObject reports whether a schema allows an object: it names no type, or names object
// among its types.
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

// addRequired adds a branch's required names to the root's, each once.
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
