package anthropicmessages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// supportsStructuredOutput reports whether model is known to support Messages output_config.format
// json_schema, per https://platform.claude.com/docs/en/build-with-claude/structured-outputs.
// Anthropic lists Claude 4.5 and later (Opus, Sonnet, Haiku, Fable, Mythos) and
// claude-mythos-preview; recognized older Claude versions (3.x, 4.0, 4.1) are not supported.
// A name it cannot place (such as claude-mythos-preview) returns true, so a working request is not
// changed.
func supportsStructuredOutput(model string) bool {
	major, minor, ok := parseClaudeVersion(model)
	if !ok {
		return true
	}
	return major > 4 || (major == 4 && minor >= 5)
}

// supportedStringFormats are the JSON Schema string formats Anthropic's structured outputs
// grammar accepts (https://platform.claude.com/docs/en/build-with-claude/structured-outputs).
var supportedStringFormats = map[string]bool{
	"date-time": true,
	"time":      true,
	"date":      true,
	"duration":  true,
	"email":     true,
	"hostname":  true,
	"uri":       true,
	"ipv4":      true,
	"ipv6":      true,
	"uuid":      true,
}

// droppedSchemaConstraints are JSON Schema validation keywords that Anthropic's structured outputs
// grammar refuses with 400 and that can be dropped with a reported loss (matching the Anthropic
// SDK's transform_schema behavior).
var droppedSchemaConstraints = map[string]bool{
	"minimum":               true,
	"maximum":               true,
	"exclusiveMinimum":      true,
	"exclusiveMaximum":      true,
	"multipleOf":            true,
	"minLength":             true,
	"maxLength":             true,
	"maxItems":              true,
	"uniqueItems":           true,
	"minContains":           true,
	"maxContains":           true,
	"contains":              true,
	"prefixItems":           true,
	"additionalItems":       true,
	"unevaluatedItems":      true,
	"minProperties":         true,
	"maxProperties":         true,
	"propertyNames":         true,
	"unevaluatedProperties": true,
}

// unrepresentableSchemaKeywords are JSON Schema keywords that Anthropic's structured outputs
// grammar refuses and that cannot be dropped without inverting or breaking the schema's meaning.
var unrepresentableSchemaKeywords = map[string]bool{
	"not":               true,
	"if":                true,
	"then":              true,
	"else":              true,
	"patternProperties": true,
	"dependentSchemas":  true,
	"dependencies":      true,
	"dependentRequired": true,
}

// unionAnnotationKeywords are keywords Anthropic allows alongside anyOf in the same schema object.
var unionAnnotationKeywords = map[string]bool{
	"anyOf":       true,
	"oneOf":       true,
	"title":       true,
	"description": true,
	"default":     true,
	"$defs":       true,
	"definitions": true,
	"$schema":     true,
	"$id":         true,
	"$comment":    true,
}

type schemaAdapter struct {
	root    map[string]any
	losses  []llm.Loss
	changed bool
	checked map[string]bool // $refs already followed and found free of cycles
}

// adaptStructuredSchema checks and adapts a cross-format JSON Schema for Messages
// output_config.format. A schema Anthropic already accepts is returned byte-identically with no
// losses. Adaptable differences (closing objects that have properties, rewriting oneOf to anyOf,
// dropping unsupported numeric/string/array/object constraints) are applied and reported as
// losses. A schema that cannot be made valid without breaking its meaning returns ok false with a
// single dropped loss on response_format.
func adaptStructuredSchema(raw json.RawMessage) (json.RawMessage, []llm.Loss, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil, []llm.Loss{{Kind: llm.LossDropped, Path: "response_format", Detail: "Messages output_config.format json_schema requires a valid schema object"}}, false
	}
	root, ok := parsed.(map[string]any)
	if !ok || root == nil {
		return nil, []llm.Loss{{Kind: llm.LossDropped, Path: "response_format", Detail: "Messages output_config.format json_schema requires a schema object"}}, false
	}
	a := &schemaAdapter{root: root, checked: map[string]bool{}}
	if reason := a.checkCycles(root, map[string]bool{}); reason != "" {
		return nil, []llm.Loss{{Kind: llm.LossDropped, Path: "response_format", Detail: reason}}, false
	}
	if reason := a.walk(root, "response_format.schema"); reason != "" {
		return nil, []llm.Loss{{Kind: llm.LossDropped, Path: "response_format", Detail: reason}}, false
	}
	if !a.changed {
		return raw, nil, true
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, []llm.Loss{{Kind: llm.LossDropped, Path: "response_format", Detail: "Messages output_config.format json_schema could not be encoded"}}, false
	}
	return out, a.losses, true
}

func (a *schemaAdapter) lose(kind, path, detail string) {
	a.losses = append(a.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

// checkCycles rejects external $refs and recursive local $ref cycles.
func (a *schemaAdapter) checkCycles(node any, visiting map[string]bool) string {
	m, ok := node.(map[string]any)
	if !ok || m == nil {
		return ""
	}
	if refVal, hasRef := m["$ref"]; hasRef {
		ref, ok := refVal.(string)
		if !ok || ref == "" {
			return "Messages output_config.format does not support invalid $ref"
		}
		if ref == "#" {
			return "Messages output_config.format does not support recursive schemas"
		}
		if !strings.HasPrefix(ref, "#/") {
			return "Messages output_config.format does not support external $ref"
		}
		if visiting[ref] {
			return "Messages output_config.format does not support recursive schemas"
		}
		// Each $ref is followed once, so a chain of definitions that each name the next several
		// times, from the client's request, costs time in its length and not exponentially.
		if target := a.resolvePointer(ref); target != nil && !a.checked[ref] {
			visiting[ref] = true
			reason := a.checkCycles(target, visiting)
			delete(visiting, ref)
			if reason != "" {
				return reason
			}
			a.checked[ref] = true
		}
	}
	for _, key := range []string{"properties", "$defs", "definitions"} {
		if sub, ok := m[key].(map[string]any); ok {
			keys := sortedKeys(sub)
			for _, k := range keys {
				if reason := a.checkCycles(sub[k], visiting); reason != "" {
					return reason
				}
			}
		}
	}
	if items, ok := m["items"]; ok {
		if reason := a.checkCycles(items, visiting); reason != "" {
			return reason
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if list, ok := m[key].([]any); ok {
			for _, item := range list {
				if reason := a.checkCycles(item, visiting); reason != "" {
					return reason
				}
			}
		}
	}
	return ""
}

func (a *schemaAdapter) resolvePointer(ref string) any {
	parts := strings.Split(strings.TrimPrefix(ref, "#/"), "/")
	var cur any = a.root
	for _, seg := range parts {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[seg]
		if !ok {
			return nil
		}
	}
	return cur
}

func (a *schemaAdapter) walk(m map[string]any, path string) string {
	// Reject unrepresentable keywords first.
	for _, k := range sortedKeys(m) {
		if unrepresentableSchemaKeywords[k] {
			return fmt.Sprintf("Messages output_config.format does not support %s", k)
		}
	}

	// Reject complex (object/array) values in enum or const.
	if enumVal, hasEnum := m["enum"]; hasEnum {
		list, ok := enumVal.([]any)
		if !ok {
			return "Messages output_config.format does not support non-array enum"
		}
		for _, v := range list {
			if !isPrimitiveJSON(v) {
				return "Messages output_config.format does not support complex values in enum"
			}
		}
	}
	if constVal, hasConst := m["const"]; hasConst && !isPrimitiveJSON(constVal) {
		return "Messages output_config.format does not support complex values in const"
	}

	// Handle oneOf / anyOf.
	_, hasAnyOf := m["anyOf"]
	_, hasOneOf := m["oneOf"]
	if hasAnyOf && hasOneOf {
		return "Messages output_config.format does not support combining anyOf and oneOf"
	}
	if hasAnyOf || hasOneOf {
		for _, k := range sortedKeys(m) {
			if !unionAnnotationKeywords[k] {
				return fmt.Sprintf("Messages output_config.format does not support anyOf alongside %s", k)
			}
		}
		if hasOneOf {
			m["anyOf"] = m["oneOf"]
			delete(m, "oneOf")
			a.changed = true
			a.lose(llm.LossApproximated, path+".oneOf", "Messages output_config.format does not support oneOf; sent as anyOf")
		}
	}

	// Check allOf restrictions: 2+ branches cannot directly contain $ref, anyOf, oneOf, or allOf,
	// nor have conflicting scalar types.
	if allOfVal, hasAllOf := m["allOf"]; hasAllOf {
		if branches, ok := allOfVal.([]any); ok && len(branches) >= 2 {
			seenType := ""
			for _, br := range branches {
				bm, ok := br.(map[string]any)
				if !ok {
					continue
				}
				for _, forbidden := range []string{"$ref", "anyOf", "oneOf", "allOf"} {
					if _, has := bm[forbidden]; has {
						return fmt.Sprintf("Messages output_config.format does not support allOf with multiple branches containing %s", forbidden)
					}
				}
				if t, ok := bm["type"].(string); ok && t != "" {
					if seenType != "" && seenType != t {
						return "Messages output_config.format does not support allOf with conflicting types"
					}
					seenType = t
				}
			}
		}
	}

	// Object additionalProperties handling.
	if isObjectSchema(m) {
		ap, hasAP := m["additionalProperties"]
		if hasAP {
			if b, ok := ap.(bool); !ok || b {
				return "Messages output_config.format requires additionalProperties: false on objects"
			}
		} else {
			props, hasProps := m["properties"].(map[string]any)
			if !hasProps || len(props) == 0 {
				return "Messages output_config.format cannot represent an open object without properties"
			}
			m["additionalProperties"] = false
			a.changed = true
			a.lose(llm.LossApproximated, path+".additionalProperties", "Messages output_config.format requires additionalProperties: false on objects")
		}
	} else if ap, hasAP := m["additionalProperties"]; hasAP {
		if b, ok := ap.(bool); !ok || b {
			return "Messages output_config.format requires additionalProperties: false"
		}
	}

	// Drop unsupported constraints deterministically.
	for _, k := range sortedKeys(m) {
		switch {
		case droppedSchemaConstraints[k]:
			delete(m, k)
			a.changed = true
			a.lose(llm.LossDropped, path+"."+k, fmt.Sprintf("Messages output_config.format does not support %s", k))
		case k == "minItems":
			if !isAllowedMinItems(m[k]) {
				delete(m, k)
				a.changed = true
				a.lose(llm.LossDropped, path+".minItems", "Messages output_config.format only supports minItems 0 or 1")
			}
		case k == "format":
			if s, ok := m[k].(string); !ok || !supportedStringFormats[s] {
				a.lose(llm.LossDropped, path+".format", fmt.Sprintf("Messages output_config.format does not support string format %q", fmt.Sprint(m[k])))
				delete(m, k)
				a.changed = true
			}
		case k == "pattern":
			if s, ok := m[k].(string); ok && unsupportedRegex(s) {
				delete(m, k)
				a.changed = true
				a.lose(llm.LossDropped, path+".pattern", "Messages output_config.format does not support this regex pattern construct")
			}
		}
	}

	// Recurse into child schemas in deterministic order.
	for _, key := range []string{"properties", "$defs", "definitions"} {
		if sub, ok := m[key].(map[string]any); ok {
			for _, k := range sortedKeys(sub) {
				if child, ok := sub[k].(map[string]any); ok {
					if reason := a.walk(child, path+"."+key+"."+k); reason != "" {
						return reason
					}
				}
			}
		}
	}
	if items, ok := m["items"].(map[string]any); ok {
		if reason := a.walk(items, path+".items"); reason != "" {
			return reason
		}
	}
	for _, key := range []string{"anyOf", "allOf"} {
		if list, ok := m[key].([]any); ok {
			for i, item := range list {
				if child, ok := item.(map[string]any); ok {
					if reason := a.walk(child, fmt.Sprintf("%s.%s[%d]", path, key, i)); reason != "" {
						return reason
					}
				}
			}
		}
	}
	return ""
}

func isObjectSchema(m map[string]any) bool {
	if _, hasProps := m["properties"]; hasProps {
		return true
	}
	switch t := m["type"].(type) {
	case string:
		return t == "object"
	case []any:
		for _, v := range t {
			if s, ok := v.(string); ok && s == "object" {
				return true
			}
		}
	}
	return false
}

func isPrimitiveJSON(v any) bool {
	switch v.(type) {
	case nil, bool, string, json.Number, float64:
		return true
	default:
		return false
	}
}

func isAllowedMinItems(v any) bool {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i == 0 || i == 1
		}
		if f, err := strconv.ParseFloat(string(n), 64); err == nil {
			return f == 0 || f == 1
		}
	case float64:
		return n == 0 || n == 1
	}
	return false
}

// unsupportedRegex reports whether pat uses a regex construct Anthropic's structured outputs
// grammar refuses: backreferences (\1..\9), lookahead/lookbehind ((?=, (?!, (?<=, (?<!), or
// word boundaries (\b, \B) outside a character class.
func unsupportedRegex(pat string) bool {
	inClass := false
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		if c == '\\' {
			if i+1 >= len(pat) {
				break
			}
			next := pat[i+1]
			if !inClass && (next == 'b' || next == 'B' || (next >= '1' && next <= '9')) {
				return true
			}
			i++
			continue
		}
		if c == '[' && !inClass {
			inClass = true
			continue
		}
		if c == ']' && inClass {
			inClass = false
			continue
		}
		if !inClass && c == '(' && i+2 < len(pat) && pat[i+1] == '?' {
			if pat[i+2] == '=' || pat[i+2] == '!' {
				return true
			}
			if pat[i+2] == '<' && i+3 < len(pat) && (pat[i+3] == '=' || pat[i+3] == '!') {
				return true
			}
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
