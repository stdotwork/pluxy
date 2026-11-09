package main

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// The Codex endpoint rejects some JSON Schema forms that clients send in a function tool's
// parameters, mostly tools from MCP servers. A rejected request fails whole, so these rules
// rewrite the schema first. Each rule only removes or replaces something the tool's client
// checks again itself, or states the same constraint in another form:
//
//   - A `pattern`, or a `patternProperties` key, that uses a regular expression construct the
//     endpoint cannot compile is removed: a Unicode property escape (\p{...}, \P{...}) or the
//     octal NUL escape (\0).
//   - A numeric bound (`minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`) that is an
//     integer outside the 64-bit range is removed.
//   - A property whose `oneOf` or `anyOf` is only distinct `const` branches becomes an `enum`.

// unionToEnumBranches is how many branches a union needs before it becomes an enum. Smaller
// unions are left as written.
const unionToEnumBranches = 8

// boundKeywords are the numeric bound keywords of JSON Schema.
var boundKeywords = [...]string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"}

// subschemaMaps are keywords whose value is an object of subschemas.
var subschemaMaps = [...]string{"properties", "$defs", "definitions", "dependentSchemas", "dependencies"}

// subschemas are keywords whose value is a subschema or a list of them.
var subschemas = [...]string{
	"items", "prefixItems", "contains", "additionalProperties", "propertyNames",
	"unevaluatedProperties", "unevaluatedItems", "additionalItems", "contentSchema",
	"anyOf", "oneOf", "allOf", "not", "if", "then", "else",
}

// fixToolSchemas applies the rules to the parameters of every function tool in tools, also
// those inside namespace tools (the Codex app groups MCP tools that way), and reports whether
// it changed any. A tool it changes is replaced in the slice; the others stay as sent.
func fixToolSchemas(tools []json.RawMessage) bool {
	changed := false
	for i, t := range tools {
		var o map[string]json.RawMessage
		if json.Unmarshal(t, &o) != nil {
			continue
		}
		var typ string
		_ = json.Unmarshal(o["type"], &typ)
		switch typ {
		case "function":
			if p, ok := fixSchema(o["parameters"]); ok {
				o["parameters"] = p
				tools[i] = raw(o)
				changed = true
			}
		case "namespace":
			var nested []json.RawMessage
			if json.Unmarshal(o["tools"], &nested) == nil && fixToolSchemas(nested) {
				o["tools"] = raw(nested)
				tools[i] = raw(o)
				changed = true
			}
		}
	}
	return changed
}

// fixSchema returns the schema with the rules applied, and whether that changed it. A schema
// that is not a JSON object is left alone. Numbers are read as text so a large bound or enum
// value keeps its digits.
func fixSchema(schema json.RawMessage) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	var root any
	if dec.Decode(&root) != nil {
		return schema, false
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return schema, false
	}
	changed := dropRejected(obj)
	if props, ok := obj["properties"].(map[string]any); ok {
		for _, p := range props {
			if prop, ok := p.(map[string]any); ok && unionToEnum(prop) {
				changed = true
			}
		}
	}
	if !changed {
		return schema, false
	}
	return raw(obj), true
}

// dropRejected removes the patterns and bounds the endpoint rejects from v and every schema
// under it, and reports whether it removed any. Only keywords that hold schemas are entered,
// so an `enum`, `const` or `default` that happens to look like a schema is not touched.
func dropRejected(v any) bool {
	changed := false
	switch s := v.(type) {
	case []any:
		for _, item := range s {
			if dropRejected(item) {
				changed = true
			}
		}
	case map[string]any:
		if p, ok := s["pattern"].(string); ok && rejectedPattern(p) {
			delete(s, "pattern")
			changed = true
		}
		for _, k := range boundKeywords {
			if n, ok := s[k].(json.Number); ok && outsideInt64(n) {
				delete(s, k)
				changed = true
			}
		}
		if pp, ok := s["patternProperties"].(map[string]any); ok {
			for k, sub := range pp {
				if rejectedPattern(k) {
					delete(pp, k)
					changed = true
				} else if dropRejected(sub) {
					changed = true
				}
			}
		}
		for _, k := range subschemaMaps {
			if m, ok := s[k].(map[string]any); ok {
				for _, sub := range m {
					if dropRejected(sub) {
						changed = true
					}
				}
			}
		}
		for _, k := range subschemas {
			switch sub := s[k].(type) {
			case map[string]any, []any:
				if dropRejected(sub) {
					changed = true
				}
			}
		}
	}
	return changed
}

// rejectedPattern reports whether a regular expression uses \p{...}, \P{...} or \0, which the
// endpoint fails to compile. Both are legal in the dialects clients write; a validator that
// follows Python's `re` does not take them.
func rejectedPattern(p string) bool {
	for i := 0; i+1 < len(p); i++ {
		if p[i] != '\\' {
			continue
		}
		switch p[i+1] {
		case '0':
			return true
		case 'p', 'P':
			if i+2 < len(p) && p[i+2] == '{' {
				return true
			}
		}
		i++ // the escaped character is not the start of another escape, as in \\p
	}
	return false
}

// unionToEnum turns a property's oneOf or anyOf into an enum when it holds at least
// unionToEnumBranches branches and every branch is a distinct scalar `const` (a `description`
// or `title` beside it does not matter), and reports whether it changed the property. Where
// the property already has an enum with the same values, the union is redundant and only the
// union is removed. A property with both a oneOf and an anyOf, an enum that differs, or any
// branch that says more than a constant is left as written: the union may mean more than a
// list of values.
func unionToEnum(prop map[string]any) bool {
	_, hasOneOf := prop["oneOf"]
	_, hasAnyOf := prop["anyOf"]
	if hasOneOf == hasAnyOf {
		return false
	}
	name := "anyOf"
	if hasOneOf {
		name = "oneOf"
	}
	branches, ok := prop[name].([]any)
	if !ok || len(branches) < unionToEnumBranches {
		return false
	}
	values := make([]any, 0, len(branches))
	keys := make(map[string]bool, len(branches))
	for _, b := range branches {
		branch, ok := b.(map[string]any)
		if !ok {
			return false
		}
		c, ok := branch["const"]
		if !ok {
			return false
		}
		for k := range branch {
			if k != "const" && k != "description" && k != "title" {
				return false
			}
		}
		key, ok := valueKey(c)
		if !ok || keys[key] {
			return false
		}
		keys[key] = true
		values = append(values, c)
	}
	if existing, ok := prop["enum"]; ok {
		list, ok := existing.([]any)
		if !ok || len(list) != len(values) {
			return false
		}
		// The union's values are distinct, so an enum of the same length whose values are all in
		// the union and distinct too is the same set.
		seen := make(map[string]bool, len(list))
		for _, e := range list {
			key, ok := valueKey(e)
			if !ok || !keys[key] || seen[key] {
				return false
			}
			seen[key] = true
		}
		delete(prop, name)
		return true
	}
	prop["enum"] = values
	delete(prop, name)
	return true
}

// valueKey names a scalar JSON value so that equal values have equal names: 1, 1.0 and 1e0 are
// one number. It reports false for an object or an array.
func valueKey(v any) (string, bool) {
	switch v := v.(type) {
	case string:
		return "s:" + v, true
	case bool:
		return "b:" + strconv.FormatBool(v), true
	case nil:
		return "null", true
	case json.Number:
		neg, digits, exp := splitNumber(v)
		if digits == "" {
			return "n:0", true
		}
		sign := ""
		if neg {
			sign = "-"
		}
		return "n:" + sign + digits + "e" + exp.String(), true
	}
	return "", false
}

// outsideInt64 reports whether n is a whole number that does not fit in an int64. A number
// with a fraction, such as 1.5, is not whole and is kept; so is 1e3, which is 1000.
func outsideInt64(n json.Number) bool {
	neg, digits, exp := splitNumber(n)
	if digits == "" || exp.Sign() < 0 {
		return false
	}
	if exp.Cmp(big.NewInt(19)) > 0 {
		return true
	}
	e := int(exp.Int64())
	if len(digits)+e > 19 {
		return true
	}
	s := digits + strings.Repeat("0", e)
	if neg {
		s = "-" + s
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err != nil
}

// splitNumber writes n (JSON number syntax, as the decoder has checked) as sign, digits and a
// power of ten: the value is ±digits × 10^exp, where digits has no leading or trailing zeros
// and is empty for zero. The exponent is a big.Int because JSON allows one of any length.
func splitNumber(n json.Number) (neg bool, digits string, exp *big.Int) {
	s := string(n)
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	exp = new(big.Int)
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp.SetString(strings.TrimPrefix(s[i+1:], "+"), 10)
		s = s[:i]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	digits = strings.TrimLeft(intPart+frac, "0")
	if digits == "" {
		return false, "", new(big.Int)
	}
	trimmed := strings.TrimRight(digits, "0")
	exp.Add(exp, big.NewInt(int64(len(digits)-len(trimmed)-len(frac))))
	return neg, trimmed, exp
}
