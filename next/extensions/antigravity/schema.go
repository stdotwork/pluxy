package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Cloud Code takes schemas in the OpenAPI subset Gemini's parameters field reads, not full JSON
// Schema. Today's core cleans every schema it sends (internal/util/gemini_schema.go), and this
// file does the same for the forms that change what Cloud Code accepts:
//
//   - local $refs are inlined and $defs removed; const becomes a one-value enum;
//   - constraints Cloud Code does not take (minLength, pattern, minimum, ...) move into the
//     description as hints ("minLength: 1"), as do enums of 2 to 10 values ("Allowed: a, b");
//   - a function's parameters lose their enums (Cloud Code does not enforce them) and
//     additionalProperties (hinted when false); a response schema keeps both, as Cloud Code
//     enforces them there, but loses a boolean's enum;
//   - a type list such as ["string","null"] becomes the one type with nullable true;
//   - a union with one non-null branch becomes that branch, nullable; for Claude models, whose
//     backend rejects unions in function declarations, every union becomes its strongest branch
//     (object over array over scalar) with the other types hinted ("Accepts: a | b");
//   - other JSON Schema keywords are removed.
//
// shortcut: today's core also adds a placeholder property ("_" or "reason") to Claude's objects
// that require nothing, and removes it from the model's function calls; add both if Claude is
// seen to refuse such objects.

// schemaMode says which rules apply to a schema.
type schemaMode struct {
	response  bool // a response schema, not a function's parameters
	claude    bool // a function declaration for a Claude model
	keepTitle bool
}

// hintedConstraints move into the description, in this order, as today's core moves them.
var hintedConstraints = []string{
	"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum", "pattern", "minItems", "maxItems",
	"uniqueItems", "contains", "format", "default", "examples", "minimum", "maximum", "multipleOf",
}

// removedKeywords are removed without a hint.
var removedKeywords = []string{
	"$schema", "$defs", "definitions", "$id", "id", "$anchor", "$vocabulary", "$dynamicRef", "$dynamicAnchor", "$comment",
	"propertyNames", "patternProperties", "dependentRequired", "dependentSchemas", "dependencies",
	"if", "then", "else", "not", "unevaluatedProperties", "unevaluatedItems", "additionalItems", "contentSchema",
	"contentMediaType", "contentEncoding", "readOnly", "writeOnly", "deprecated", "discriminator",
	"enumDescriptions", "enumTitles", "prefill", "encrypted",
}

// maxRefDepth bounds $ref inlining, so a schema that refers to itself ends.
const maxRefDepth = 16

// cleanTools returns a request's tools with every function declaration's schemas cleaned: its
// parameters (parametersJsonSchema renamed to parameters) and its response.
func cleanTools(raw json.RawMessage, claude, keepTitle bool) json.RawMessage {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return raw
	}
	for _, t := range tools {
		for _, key := range []string{"functionDeclarations", "function_declarations"} {
			var decls []map[string]json.RawMessage
			if json.Unmarshal(t[key], &decls) != nil {
				continue
			}
			for _, d := range decls {
				if js, ok := d["parametersJsonSchema"]; ok {
					if _, has := d["parameters"]; !has {
						d["parameters"] = js
					}
					delete(d, "parametersJsonSchema")
				}
				if p, ok := d["parameters"]; ok {
					d["parameters"] = cleanSchema(p, schemaMode{claude: claude, keepTitle: keepTitle})
				}
				for _, rk := range []string{"responseJsonSchema", "response_json_schema"} {
					if r, ok := d[rk]; ok {
						if _, has := d["response"]; !has {
							d["response"] = r
						}
						delete(d, rk)
					}
				}
				if r, ok := d["response"]; ok {
					d["response"] = cleanSchema(r, schemaMode{response: true})
				}
			}
			t[key], _ = json.Marshal(decls)
		}
	}
	out, err := json.Marshal(tools)
	if err != nil {
		return raw
	}
	return out
}

// cleanResponseSchema moves a generation config's response schema to responseSchema, as today's
// core sends it, and cleans it.
func cleanResponseSchema(gen map[string]json.RawMessage) bool {
	changed := false
	for _, k := range []string{"responseJsonSchema", "response_json_schema", "response_schema"} {
		if v, ok := gen[k]; ok {
			if _, has := gen["responseSchema"]; !has {
				gen["responseSchema"] = v
			}
			delete(gen, k)
			changed = true
		}
	}
	if v, ok := gen["responseSchema"]; ok {
		gen["responseSchema"] = cleanSchema(v, schemaMode{response: true})
		changed = true
	}
	return changed
}

// cleanSchema returns a schema cleaned under mode.
func cleanSchema(raw json.RawMessage, mode schemaMode) json.RawMessage {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return raw
	}
	defs := map[string]any{}
	if m, ok := root.(map[string]any); ok {
		for _, k := range []string{"$defs", "definitions"} {
			if d, ok := m[k].(map[string]any); ok {
				for name, v := range d {
					defs["#/"+k+"/"+name] = v
				}
			}
		}
	}
	out, err := json.Marshal(clean(root, defs, mode, 0))
	if err != nil {
		return raw
	}
	return out
}

func clean(v any, defs map[string]any, mode schemaMode, depth int) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = clean(val, defs, mode, depth)
		}
		return out
	case map[string]any:
		return cleanObject(x, defs, mode, depth)
	}
	return v
}

func cleanObject(x map[string]any, defs map[string]any, mode schemaMode, depth int) any {
	if ref, ok := x["$ref"].(string); ok {
		target, found := defs[ref]
		if !found || depth >= maxRefDepth {
			// An unknown or too deep reference becomes a plain object.
			return map[string]any{"type": "object"}
		}
		merged := map[string]any{}
		if t, ok := target.(map[string]any); ok {
			for k, val := range t {
				merged[k] = val
			}
		}
		for k, val := range x {
			if k != "$ref" {
				merged[k] = val
			}
		}
		return cleanObject(merged, defs, mode, depth+1)
	}
	out := map[string]any{}
	for k, val := range x {
		switch {
		case k == "properties":
			// Property names are not keywords: keep every one.
			if props, ok := val.(map[string]any); ok {
				cleaned := map[string]any{}
				for name, p := range props {
					cleaned[name] = clean(p, defs, mode, depth)
				}
				out[k] = cleaned
				continue
			}
			out[k] = val
		case k == "title" && !mode.keepTitle:
		case slices.Contains(removedKeywords, k):
		default:
			out[k] = clean(val, defs, mode, depth)
		}
	}
	if c, ok := out["const"]; ok {
		if _, has := out["enum"]; !has {
			out["enum"] = []any{c}
		}
		delete(out, "const")
	}
	// Hints, in today's core's order: enums, additionalProperties, constraints.
	if enum, ok := out["enum"].([]any); ok {
		if len(enum) > 1 && len(enum) <= 10 {
			addHint(out, "Allowed: "+joinValues(enum, ", "))
		}
		if !mode.response || out["type"] == "boolean" {
			if len(enum) == 1 {
				addHint(out, "Allowed: "+valueString(enum[0]))
			}
			delete(out, "enum")
		}
	}
	if ap, ok := out["additionalProperties"]; ok {
		if ap == false && !mode.response {
			addHint(out, "No extra properties allowed")
		}
		if ap != false || !mode.response {
			delete(out, "additionalProperties")
		}
	}
	for _, k := range hintedConstraints {
		if val, ok := out[k]; ok {
			addHint(out, k+": "+valueString(val))
			delete(out, k)
		}
	}
	for _, uk := range []string{"anyOf", "oneOf"} {
		if branches, ok := out[uk].([]any); ok {
			if sel := flattenUnion(out, branches, mode); sel != nil {
				return sel
			}
		}
	}
	if types, ok := out["type"].([]any); ok {
		var nonNull []string
		null := false
		for _, t := range types {
			if s, _ := t.(string); s == "null" {
				null = true
			} else if s != "" {
				nonNull = append(nonNull, s)
			}
		}
		if len(nonNull) > 0 {
			out["type"] = nonNull[0]
		} else {
			delete(out, "type")
		}
		if len(nonNull) > 1 {
			addHint(out, "Accepts: "+strings.Join(nonNull, " | "))
		}
		if null {
			out["nullable"] = true
			if len(nonNull) <= 1 {
				addHint(out, "(nullable)")
			}
		}
	}
	return out
}

// flattenUnion returns the schema a union becomes, or nil when it stays a union: a union with one
// non-null branch becomes that branch, and for Claude every union becomes its strongest branch.
func flattenUnion(parent map[string]any, branches []any, mode schemaMode) map[string]any {
	var nonNull []map[string]any
	null := false
	for _, b := range branches {
		m, _ := b.(map[string]any)
		if m == nil {
			continue
		}
		if m["type"] == "null" {
			null = true
			continue
		}
		nonNull = append(nonNull, m)
	}
	if len(nonNull) == 0 || (len(nonNull) > 1 && !mode.claude) {
		return nil
	}
	best, types := 0, []string{}
	bestScore := -1
	for i, b := range nonNull {
		score, t := branchScore(b)
		if t != "" {
			types = append(types, t)
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if null {
		types = append(types, "null")
	}
	sel := map[string]any{}
	for k, v := range nonNull[best] {
		sel[k] = v
	}
	if null {
		sel["nullable"] = true
	}
	if d, _ := parent["description"].(string); d != "" {
		if own, _ := sel["description"].(string); own == "" {
			sel["description"] = d
		} else if own != d {
			sel["description"] = d + " (" + own + ")"
		}
	}
	switch {
	case len(types) > 1 && len(nonNull) > 1:
		addHint(sel, "Accepts: "+strings.Join(types, " | "))
	case null:
		addHint(sel, "(nullable)")
	}
	return sel
}

// branchScore ranks a union branch as today's core does: object 3, array 2, other types 1.
func branchScore(b map[string]any) (int, string) {
	t, _ := b["type"].(string)
	switch {
	case t == "object" || b["properties"] != nil:
		return 3, "object"
	case t == "array" || b["items"] != nil:
		return 2, "array"
	case t != "":
		return 1, t
	}
	return 0, ""
}

// addHint appends hint to a schema's description as today's core does: alone when there is none,
// otherwise in parentheses after it, and never twice.
func addHint(m map[string]any, hint string) {
	d, _ := m["description"].(string)
	switch {
	case d == "":
		m["description"] = hint
	case d == hint || strings.HasPrefix(d, hint+" (") || strings.Contains(d, "("+hint+")"):
	default:
		m["description"] = fmt.Sprintf("%s (%s)", d, hint)
	}
}

func joinValues(vals []any, sep string) string {
	s := make([]string, len(vals))
	for i, v := range vals {
		s[i] = valueString(v)
	}
	return strings.Join(s, sep)
}

// valueString writes a schema value in a hint: a string as it is, anything else as JSON.
func valueString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
