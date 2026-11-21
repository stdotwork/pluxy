package geminigenerate

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	"github.com/stdotwork/pluxy/next/llm"
)

// Tool names. GenerateContent takes a function name of 1 to 128 characters from a-z, A-Z, 0-9,
// underscores, dots, colons and dashes, and Vertex AI also wants it to start with a letter or an
// underscore (https://ai.google.dev/api/caching#FunctionDeclaration and
// https://cloud.google.com/vertex-ai/docs/reference/rest/v1beta1/FunctionDeclaration). A client
// in another format may use other characters (MCP tools often have slashes), so such a name is
// sent under a GenerateContent name and the reply's calls go back to the client under its own.
const maxToolName = 128

// validToolName reports whether GenerateContent takes name as it is, on both the Gemini API and
// Vertex AI.
func validToolName(name string) bool {
	if name == "" || len(name) > maxToolName {
		return false
	}
	if c := name[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_') {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !toolNameByte(name[i]) {
			return false
		}
	}
	return true
}

// toolNameByte reports whether c is one of the characters GenerateContent takes in a function
// name.
func toolNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '.' || c == ':' || c == '-'
}

// sanitizeToolName replaces each character GenerateContent does not take with an underscore,
// starts the name with an underscore when it does not start with a letter or underscore, and
// cuts it to 128 bytes.
func sanitizeToolName(name string) string {
	b := []byte(name)
	for i, c := range b {
		if !toolNameByte(c) {
			b[i] = '_'
		}
	}
	if len(b) > maxToolName {
		b = b[:maxToolName]
	}
	if len(b) > 0 && b[0] != '_' && !(b[0] >= 'a' && b[0] <= 'z' || b[0] >= 'A' && b[0] <= 'Z') {
		if len(b) == maxToolName {
			b = b[:maxToolName-1]
		}
		b = append([]byte{'_'}, b...)
	}
	return string(b)
}

// ToolNames returns the GenerateContent name of each function name in req that GenerateContent
// does not take as it is, keyed by the client's name: the tools' names first, then the names of
// the tool calls replayed in its items. Every name sent is distinct. A name is kept when it is
// valid and not taken, otherwise sanitized (each other character an underscore, at most 128
// bytes); when two sanitized names would be the same, or one is already taken, each gets a hash
// of the client's name after the first 117 bytes. The result depends only on req, so the request
// and its reply agree.
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
		if len(base) > 117 {
			base = base[:117]
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

// FunctionTools returns the function tools EncodeRequest declares for req, each under the name the
// reply's calls go back to the client with (a Responses client's custom and namespace tools are
// sent as functions).
func FunctionTools(req llm.Request) []llm.Tool {
	req, _ = clienttools.Lower(req)
	return req.Tools
}

// ToolName returns the GenerateContent name for a client's function name.
func ToolName(names map[string]string, name string) string {
	if to, ok := names[name]; ok {
		return to
	}
	if name == "" || validToolName(name) {
		return name
	}
	return sanitizeToolName(name)
}
