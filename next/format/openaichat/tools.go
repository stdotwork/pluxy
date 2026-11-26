package openaichat

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	"github.com/stdotwork/pluxy/next/llm"
)

// Tool names. Chat Completions takes a function name of 1 to 64 characters from a-z, A-Z, 0-9,
// underscores and dashes (https://platform.openai.com/docs/api-reference/chat/create). A client
// in another format may use other characters (MCP tools often have dots or colons), so such a
// name is sent under a Chat name and the reply's calls go back to the client under its own.
const maxToolName = 64

// validToolName reports whether Chat Completions takes name as it is.
func validToolName(name string) bool {
	if name == "" || len(name) > maxToolName {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// sanitizeToolName replaces each character Chat Completions does not take with an underscore and
// cuts the name to 64 bytes.
func sanitizeToolName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	if len(b) > maxToolName {
		b = b[:maxToolName]
	}
	return string(b)
}

// ToolNames returns the Chat name of each function name in req that Chat Completions does not
// take as it is, keyed by the client's name: the tools' names first, then the names of the tool
// calls replayed in its items. Every name sent is distinct. A name is kept when it is valid and
// not taken, otherwise sanitized (each other character an underscore, at most 64 bytes); when two
// sanitized names would be the same, or one is already taken, each gets a hash of the client's
// name after the first 53 bytes. The result depends only on req, so the request and its reply
// agree.
func ToolNames(req llm.Request) map[string]string {
	req, _ = clienttools.Lower(req) // the names the encoder sends
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

// ToolName returns the Chat name for a client's function name.
func ToolName(names map[string]string, name string) string {
	if to, ok := names[name]; ok {
		return to
	}
	if name == "" || validToolName(name) {
		return name
	}
	return sanitizeToolName(name)
}
