package main

import (
	"encoding/json"
	"slices"
)

// Cloud Code takes function declarations' schemas in the OpenAPI subset Gemini's parameters field
// reads, not full JSON Schema: today's core renames parametersJsonSchema to parameters and
// cleans each schema before sending it. cleanTools does the same for the keywords that make Cloud
// Code refuse a request: local $refs are inlined, const becomes a one-value enum, and keywords
// outside the subset are removed.
//
// shortcut: today's core also folds removed constraints (minLength, pattern, ...) into the
// description as hints and flattens anyOf for Claude models; neither changes whether a request is
// accepted. Add them if a model is seen to need the hints.

// unsupportedKeywords are the JSON Schema keywords removed from a declaration's schema.
var unsupportedKeywords = []string{
	"$schema", "$defs", "definitions", "$id", "id", "$anchor", "$vocabulary", "$dynamicRef", "$dynamicAnchor", "$comment",
	"additionalProperties", "propertyNames", "patternProperties", "dependentRequired", "dependentSchemas", "dependencies",
	"if", "then", "else", "not", "unevaluatedProperties", "unevaluatedItems", "additionalItems", "contentSchema",
	"contentMediaType", "contentEncoding", "readOnly", "writeOnly", "deprecated", "discriminator", "examples", "default",
	"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum", "pattern", "minItems", "maxItems", "uniqueItems",
	"contains", "format", "title", "enumDescriptions", "enumTitles", "prefill", "encrypted",
}

// maxRefDepth bounds $ref inlining, so a schema that refers to itself ends.
const maxRefDepth = 16

// cleanTools returns a request's tools with every function declaration's schema cleaned.
func cleanTools(raw json.RawMessage) json.RawMessage {
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
					d["parameters"] = cleanSchema(p)
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

// cleanSchema returns schema with its local $refs inlined and unsupported keywords removed.
func cleanSchema(raw json.RawMessage) json.RawMessage {
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
	out, err := json.Marshal(clean(root, defs, 0))
	if err != nil {
		return raw
	}
	return out
}

func clean(v any, defs map[string]any, depth int) any {
	switch x := v.(type) {
	case map[string]any:
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
			return clean(merged, defs, depth+1)
		}
		out := map[string]any{}
		for k, val := range x {
			if slices.Contains(unsupportedKeywords, k) {
				continue
			}
			switch k {
			case "properties":
				// Property names are not keywords: keep every one.
				if props, ok := val.(map[string]any); ok {
					cleaned := map[string]any{}
					for name, p := range props {
						cleaned[name] = clean(p, defs, depth)
					}
					out[k] = cleaned
					continue
				}
			case "const":
				if _, has := x["enum"]; !has {
					out["enum"] = []any{val}
				}
				continue
			}
			out[k] = clean(val, defs, depth)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = clean(val, defs, depth)
		}
		return out
	}
	return v
}
