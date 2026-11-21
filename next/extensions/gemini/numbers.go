package main

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
)

// integralArguments rewrites the arguments of one function call the model made so a number the
// request's tool schema declares integer, written with a zero fraction (2500.0), is an integer
// (2500). Gemini sends a call's arguments as a protobuf Struct, whose numbers are doubles, so an
// integer parameter comes back with a fraction. Only a property the schema itself declares
// integer is touched, in nested objects (properties) and array items; a number, a schema the
// request does not hold (a $ref, say), a value with a fraction, and one beyond 2^53 are left as
// they are, and every other byte and the key order stay as they came.
func integralArguments(args []byte, schema json.RawMessage) []byte {
	if !json.Valid(args) {
		return args
	}
	root := schemaOf(schema)
	if root == nil {
		return args
	}
	var out bytes.Buffer
	out.Grow(len(args))
	if end := rewriteJSON(args, 0, &out, root); end != len(args) {
		return args
	}
	return out.Bytes()
}

// schemaOf reads the parts of a JSON schema this repair needs: whether it is an integer, its
// properties and the schema of its array items. It returns nil when the schema says none of them
// (a number, a string, a $ref), so nothing under it is touched.
func schemaOf(raw json.RawMessage) *argSchema {
	var m map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || m == nil {
		return nil
	}
	s := &argSchema{integer: integerType(m["type"])}
	if p, ok := m["properties"]; ok {
		var props map[string]json.RawMessage
		if json.Unmarshal(p, &props) == nil {
			for name, child := range props {
				if c := schemaOf(child); c != nil {
					if s.properties == nil {
						s.properties = map[string]*argSchema{}
					}
					s.properties[name] = c
				}
			}
		}
	}
	if items, ok := m["items"]; ok {
		s.items = schemaOf(items)
	}
	if !s.integer && len(s.properties) == 0 && s.items == nil {
		return nil
	}
	return s
}

// argSchema is what integralArguments reads of a property's schema.
type argSchema struct {
	integer    bool
	properties map[string]*argSchema
	items      *argSchema
}

// integerType reports whether a schema's type is integer, or a list holding integer and null
// only: a nullable integer is still an integer.
func integerType(raw json.RawMessage) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == "integer"
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return false
	}
	integer := false
	for _, typ := range list {
		switch typ {
		case "integer":
			integer = true
		case "null":
		default:
			return false
		}
	}
	return integer
}

// rewriteJSON copies one JSON value from src starting at i to out, rewriting the numbers the
// schema declares integer, and returns the index after it. The bytes of everything else are
// copied as they are.
func rewriteJSON(src []byte, i int, out *bytes.Buffer, s *argSchema) int {
	i = copySpace(src, i, out)
	if i >= len(src) {
		return i
	}
	switch src[i] {
	case '{':
		out.WriteByte('{')
		i = copySpace(src, i+1, out)
		if i < len(src) && src[i] == '}' {
			out.WriteByte('}')
			return i + 1
		}
		for {
			i = copySpace(src, i, out)
			var key string
			i = copyString(src, i, out, &key)
			i = copySpace(src, i, out)
			if i < len(src) && src[i] == ':' {
				out.WriteByte(':')
				i++
			}
			var child *argSchema
			if s != nil {
				child = s.properties[key]
			}
			i = rewriteJSON(src, i, out, child)
			i = copySpace(src, i, out)
			if i < len(src) && src[i] == ',' {
				out.WriteByte(',')
				i++
				continue
			}
			if i < len(src) && src[i] == '}' {
				out.WriteByte('}')
				return i + 1
			}
			return i
		}
	case '[':
		out.WriteByte('[')
		i = copySpace(src, i+1, out)
		if i < len(src) && src[i] == ']' {
			out.WriteByte(']')
			return i + 1
		}
		for {
			var child *argSchema
			if s != nil {
				child = s.items
			}
			i = rewriteJSON(src, i, out, child)
			i = copySpace(src, i, out)
			if i < len(src) && src[i] == ',' {
				out.WriteByte(',')
				i++
				continue
			}
			if i < len(src) && src[i] == ']' {
				out.WriteByte(']')
				return i + 1
			}
			return i
		}
	case '"':
		return copyString(src, i, out, nil)
	default:
		start := i
		for i < len(src) && src[i] != ',' && src[i] != '}' && src[i] != ']' && !space(src[i]) {
			i++
		}
		if s != nil && s.integer && i > start {
			if n, ok := integralNumber(src[start:i]); ok {
				out.WriteString(n)
				return i
			}
		}
		out.Write(src[start:i])
		return i
	}
}

// copySpace copies the whitespace at src starting at i, and returns the index after it.
func copySpace(src []byte, i int, out *bytes.Buffer) int {
	for i < len(src) && space(src[i]) {
		out.WriteByte(src[i])
		i++
	}
	return i
}

func space(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// copyString copies the JSON string at src starting at i, and returns the index after it. key,
// when not nil, receives the decoded string.
func copyString(src []byte, i int, out *bytes.Buffer, key *string) int {
	start := i
	if i < len(src) && src[i] == '"' {
		i++
		for i < len(src) {
			switch src[i] {
			case '\\':
				i += 2
				continue
			case '"':
				i++
				out.Write(src[start:i])
				if key != nil {
					_ = json.Unmarshal(src[start:i], key)
				}
				return i
			}
			i++
		}
	}
	out.Write(src[start:])
	return len(src)
}

// integralNumber returns the integer a number literal written with a zero fraction stands for,
// when it does: 2500.0 and 2500e0 give 2500, while 2500.5, a literal already written as an
// integer, and one beyond 2^53 do not. The literal is read exactly, so a fraction too small to
// show in a float64 is still a fraction.
func integralNumber(lit []byte) (string, bool) {
	s := string(lit)
	if !strings.ContainsAny(s, ".eE") {
		return "", false // already an integer
	}
	lower := strings.ToLower(s)
	mantissa := lower
	if e := strings.IndexByte(lower, 'e'); e >= 0 {
		mantissa = lower[:e]
	}
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		for _, c := range mantissa[dot+1:] {
			if c != '0' {
				return "", false
			}
		}
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || !r.IsInt() {
		return "", false
	}
	n := new(big.Int).Abs(r.Num())
	if n.Cmp(big.NewInt(1<<53)) > 0 {
		return "", false
	}
	if n.Sign() == 0 && strings.HasPrefix(s, "-") {
		return "", false // keep -0.0 as the upstream wrote it
	}
	return r.Num().String(), true
}

// toolSchemas returns the parameters schema of each of the request's function tools, by the
// client's name.
func toolSchemas(req llm.Request) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, t := range geminigenerate.FunctionTools(req) {
		if t.Type != llm.ToolNative && len(t.Parameters) > 0 {
			out[t.Name] = t.Parameters
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
