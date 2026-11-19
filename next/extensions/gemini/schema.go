package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Tool declarations sent to the Gemini API and Vertex AI have tighter limits than full JSON
// Schema Draft 2020-12:
//
//   - Function names allow [a-zA-Z0-9_.:-] up to 128 chars on the Gemini API, and must match
//     ^[a-zA-Z_][a-zA-Z0-9_.:-]{0,63}$ on Vertex AI (at most 64 chars).
//   - Tool parameter roots must describe an object without top-level anyOf/oneOf unions.
//   - Local $ref pointers ($defs, definitions, ...) must be inlined and $defs/definitions stripped,
//     without mutating literal data values (default, example, examples, enum, const, required)
//     or confusing object property names with schema keywords.
//   - Unsupported keywords ($schema, $id, $anchor, $comment, not, patternProperties,
//     propertyNames, dependent*, unevaluated*, content*, readOnly, writeOnly, deprecated,
//     discriminator, x-*, etc.), conditional if/then/else, and allOf are resolved or stripped.
//   - Boolean subschemas (such as "items": true or union branch false) are normalized to schema
//     objects or pruned, while "additionalProperties": false/true is preserved on JSON Schema
//     carriers (parametersJsonSchema, responseJsonSchema).
//   - Array schemas must carry items; non-array schemas must not; const becomes enum; empty,
//     null, boolean, and non-string enums are normalized so enum values are non-empty strings.
//
// Each schema field keeps its wire name (parametersJsonSchema vs parameters, responseJsonSchema vs
// response / responseSchema) and valid uppercase OpenAPI types such as "OBJECT"/"STRING" stay as
// the client sent them.

const (
	maxRefDepth        = 16
	geminiMaxFuncName  = 128
	vertexMaxFuncName  = 64
	funcNameHashDigits = 10
)

type schemaOpts struct {
	isToolParams bool
	isOpenAPI    bool
}

// removedKeywords are stripped everywhere inside schema objects (outside property name maps and
// literal value fields).
var removedKeywords = map[string]bool{
	"$schema":               true,
	"$defs":                 true,
	"definitions":           true,
	"$id":                   true,
	"id":                    true,
	"$anchor":               true,
	"$vocabulary":           true,
	"$dynamicRef":           true,
	"$dynamicAnchor":        true,
	"$comment":              true,
	"$ref":                  true,
	"propertyNames":         true,
	"patternProperties":     true,
	"dependentRequired":     true,
	"dependentSchemas":      true,
	"dependencies":          true,
	"not":                   true,
	"unevaluatedProperties": true,
	"unevaluatedItems":      true,
	"additionalItems":       true,
	"contentSchema":         true,
	"contentMediaType":      true,
	"contentEncoding":       true,
	"readOnly":              true,
	"writeOnly":             true,
	"deprecated":            true,
	"discriminator":         true,
	"enumDescriptions":      true,
	"enumTitles":            true,
	"prefill":               true,
	"encrypted":             true,
}

// openAPIHintedConstraints are valid in JSON Schema Draft 2020-12 (parametersJsonSchema /
// responseJsonSchema) but absent from Vertex/Gemini's OpenAPI Schema type; on OpenAPI carriers
// they move into description hints before removal.
var openAPIHintedConstraints = []string{
	"exclusiveMinimum",
	"exclusiveMaximum",
	"multipleOf",
	"uniqueItems",
	"contains",
	"examples",
}

// literalKeywords hold raw JSON data (or lists of field names), never nested subschemas.
var literalKeywords = map[string]bool{
	"default":           true,
	"example":           true,
	"examples":          true,
	"enum":              true,
	"const":             true,
	"required":          true,
	"propertyOrdering":  true,
	"dependentRequired": true,
}

// integerConstraintKeywords hold non-negative integer constraints that some MCP servers emit as
// decimal strings (such as "minLength": "1").
var integerConstraintKeywords = map[string]bool{
	"minLength":     true,
	"maxLength":     true,
	"minItems":      true,
	"maxItems":      true,
	"minProperties": true,
	"maxProperties": true,
}

// schemaKeywords identify an object as a JSON Schema node rather than a bare map of properties.
var schemaKeywords = map[string]bool{
	"type":                 true,
	"properties":           true,
	"$ref":                 true,
	"anyOf":                true,
	"oneOf":                true,
	"allOf":                true,
	"items":                true,
	"prefixItems":          true,
	"enum":                 true,
	"const":                true,
	"additionalProperties": true,
	"if":                   true,
	"then":                 true,
	"else":                 true,
	"not":                  true,
	"$schema":              true,
	"$defs":                true,
	"definitions":          true,
	"title":                true,
	"description":          true,
	"required":             true,
	"nullable":             true,
}

// cleanRequestSchemas cleans every tool parameter schema, response schema, and function name in a
// GenerateContent (or wrapped countTokens generateContentRequest) JSON body. It returns the body
// unchanged when nothing changes, and a restore map (sanitized upstream name -> client's name)
// when any function name was rewritten.
func cleanRequestSchemas(body []byte, vertex bool) ([]byte, map[string]string) {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return body, nil
	}
	if innerRaw, ok := top["generateContentRequest"]; ok && len(bytes.TrimSpace(innerRaw)) > 0 && string(bytes.TrimSpace(innerRaw)) != "null" {
		cleanedInner, restore := cleanRequestObject(innerRaw, vertex)
		if restore == nil && bytes.Equal(cleanedInner, innerRaw) {
			return body, nil
		}
		top["generateContentRequest"] = cleanedInner
		if out, ok := encodeRawJSON(body, top); ok {
			return out, restore
		}
		return body, restore
	}
	return cleanRequestObject(body, vertex)
}

func cleanRequestObject(body []byte, vertex bool) ([]byte, map[string]string) {
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil || req == nil {
		return body, nil
	}

	declared, called := collectRequestFunctionNames(req)
	toUpstream, restore := buildFunctionNameMap(declared, called, vertex)

	changed := false
	if toolsRaw, ok := req["tools"]; ok {
		if cleanedTools, toolsChanged := cleanToolsField(toolsRaw, toUpstream); toolsChanged {
			req["tools"] = cleanedTools
			changed = true
		}
	}
	if len(toUpstream) > 0 {
		for _, tcKey := range []string{"toolConfig", "tool_config"} {
			if tcRaw, ok := req[tcKey]; ok {
				if rewritten, tcChanged := rewriteToolConfigNames(tcRaw, toUpstream); tcChanged {
					req[tcKey] = rewritten
					changed = true
				}
			}
		}
		if contentsRaw, ok := req["contents"]; ok {
			if rewritten, cChanged := rewriteContentsFunctionNames(contentsRaw, toUpstream); cChanged {
				req["contents"] = rewritten
				changed = true
			}
		}
	}
	for _, genKey := range []string{"generationConfig", "generation_config"} {
		if genRaw, ok := req[genKey]; ok {
			if cleanedGen, genChanged := cleanGenerationConfig(genRaw); genChanged {
				req[genKey] = cleanedGen
				changed = true
			}
		}
	}
	if !changed {
		return body, restore
	}
	out, ok := encodeRawJSON(body, req)
	if !ok {
		return body, restore
	}
	return out, restore
}

func collectRequestFunctionNames(req map[string]json.RawMessage) (declared, called []string) {
	if toolsRaw, ok := req["tools"]; ok {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(toolsRaw, &tools) == nil {
			for _, tool := range tools {
				for _, declKey := range []string{"functionDeclarations", "function_declarations"} {
					var decls []map[string]json.RawMessage
					if json.Unmarshal(tool[declKey], &decls) != nil {
						continue
					}
					for _, d := range decls {
						var name string
						if json.Unmarshal(d["name"], &name) == nil && name != "" {
							declared = append(declared, name)
						}
					}
				}
			}
		}
	}
	for _, tcKey := range []string{"toolConfig", "tool_config"} {
		var tc map[string]json.RawMessage
		if json.Unmarshal(req[tcKey], &tc) != nil {
			continue
		}
		for _, fcKey := range []string{"functionCallingConfig", "function_calling_config"} {
			var fc map[string]json.RawMessage
			if json.Unmarshal(tc[fcKey], &fc) != nil {
				continue
			}
			for _, allowKey := range []string{"allowedFunctionNames", "allowed_function_names"} {
				var names []string
				if json.Unmarshal(fc[allowKey], &names) == nil {
					for _, name := range names {
						if name != "" {
							called = append(called, name)
						}
					}
				}
			}
		}
	}
	if contentsRaw, ok := req["contents"]; ok {
		var contents []map[string]json.RawMessage
		if json.Unmarshal(contentsRaw, &contents) == nil {
			for _, turn := range contents {
				var parts []map[string]json.RawMessage
				if json.Unmarshal(turn["parts"], &parts) != nil {
					continue
				}
				for _, part := range parts {
					for _, fnKey := range []string{"functionCall", "function_call", "functionResponse", "function_response"} {
						var fn map[string]json.RawMessage
						if json.Unmarshal(part[fnKey], &fn) != nil {
							continue
						}
						var name string
						if json.Unmarshal(fn["name"], &name) == nil && name != "" {
							called = append(called, name)
						}
					}
				}
			}
		}
	}
	return declared, called
}

func buildFunctionNameMap(declared, called []string, vertex bool) (toUpstream, restore map[string]string) {
	if len(declared) == 0 && len(called) == 0 {
		return nil, nil
	}
	to := map[string]string{}
	taken := map[string]bool{}
	maxLen := geminiMaxFuncName
	if vertex {
		maxLen = vertexMaxFuncName
	}

	allocate := func(names []string) {
		var invalid []string
		seen := map[string]bool{}
		for _, name := range names {
			if _, done := to[name]; done || name == "" || seen[name] {
				continue
			}
			seen[name] = true
			if validFunctionName(name, vertex) && !taken[name] {
				to[name] = name
				taken[name] = true
				continue
			}
			invalid = append(invalid, name)
		}
		counts := map[string]int{}
		for _, name := range invalid {
			counts[sanitizeFunctionName(name, vertex)]++
		}
		var hashed []string
		for _, name := range invalid {
			base := sanitizeFunctionName(name, vertex)
			if counts[base] == 1 && !taken[base] {
				to[name] = base
				taken[base] = true
			} else {
				hashed = append(hashed, name)
			}
		}
		slices.Sort(hashed)
		for _, name := range hashed {
			base := sanitizeFunctionName(name, vertex)
			prefixMax := maxLen - 1 - funcNameHashDigits
			if len(base) > prefixMax {
				base = base[:prefixMax]
			}
			for i := 0; ; i++ {
				seed := name
				if i > 0 {
					seed += "\x00" + strconv.Itoa(i)
				}
				sum := sha256.Sum256([]byte(seed))
				cand := base + "_" + hex.EncodeToString(sum[:])[:funcNameHashDigits]
				if !taken[cand] {
					to[name] = cand
					taken[cand] = true
					break
				}
			}
		}
	}

	allocate(declared)
	allocate(called)

	for orig, mapped := range to {
		if orig == mapped {
			continue
		}
		if toUpstream == nil {
			toUpstream = map[string]string{}
			restore = map[string]string{}
		}
		toUpstream[orig] = mapped
		restore[mapped] = orig
	}
	return toUpstream, restore
}

func validFunctionName(name string, vertex bool) bool {
	if name == "" {
		return false
	}
	maxLen := geminiMaxFuncName
	if vertex {
		maxLen = vertexMaxFuncName
		if !isAlphaOrUnderscore(name[0]) {
			return false
		}
	}
	if len(name) > maxLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isAllowedFuncChar(name[i]) {
			return false
		}
	}
	return true
}

func sanitizeFunctionName(name string, vertex bool) string {
	var b strings.Builder
	b.Grow(len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isAllowedFuncChar(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "tool"
	}
	maxLen := geminiMaxFuncName
	if vertex {
		maxLen = vertexMaxFuncName
		if !isAlphaOrUnderscore(out[0]) {
			out = "_" + out
		}
	}
	if len(out) > maxLen {
		out = out[:maxLen]
	}
	return out
}

func isAlphaOrUnderscore(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || b == '_'
}

func isAllowedFuncChar(b byte) bool {
	return isAlphaOrUnderscore(b) || (b >= '0' && b <= '9') || b == '.' || b == ':' || b == '-'
}

func cleanToolsField(raw json.RawMessage, toUpstream map[string]string) (json.RawMessage, bool) {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 {
		return raw, false
	}
	changed := false
	for i, tool := range tools {
		toolChanged := false
		for _, declKey := range []string{"functionDeclarations", "function_declarations"} {
			declsRaw, ok := tool[declKey]
			if !ok {
				continue
			}
			var decls []map[string]json.RawMessage
			if json.Unmarshal(declsRaw, &decls) != nil {
				continue
			}
			declsChanged := false
			for j, decl := range decls {
				if decl == nil {
					continue
				}
				dChanged := false
				if len(toUpstream) > 0 {
					var name string
					if json.Unmarshal(decl["name"], &name) == nil {
						if mapped, ok := toUpstream[name]; ok && mapped != name {
							if encoded, ok := encodeRawJSON(decl["name"], mapped); ok {
								decl["name"] = encoded
								dChanged = true
							}
						}
					}
				}
				for _, pk := range []string{"parametersJsonSchema", "parameters_json_schema"} {
					if v, ok := decl[pk]; ok && !isNullRaw(v) {
						cleaned, c := cleanSchemaBytes(v, schemaOpts{isToolParams: true, isOpenAPI: false})
						if c {
							decl[pk] = cleaned
							dChanged = true
						}
					}
				}
				if v, ok := decl["parameters"]; ok && !isNullRaw(v) {
					cleaned, c := cleanSchemaBytes(v, schemaOpts{isToolParams: true, isOpenAPI: true})
					if c {
						decl["parameters"] = cleaned
						dChanged = true
					}
				}
				for _, rk := range []string{"responseJsonSchema", "response_json_schema"} {
					if v, ok := decl[rk]; ok && !isNullRaw(v) {
						cleaned, c := cleanSchemaBytes(v, schemaOpts{isToolParams: false, isOpenAPI: false})
						if c {
							decl[rk] = cleaned
							dChanged = true
						}
					}
				}
				if v, ok := decl["response"]; ok && !isNullRaw(v) {
					cleaned, c := cleanSchemaBytes(v, schemaOpts{isToolParams: false, isOpenAPI: true})
					if c {
						decl["response"] = cleaned
						dChanged = true
					}
				}
				if dChanged {
					decls[j] = decl
					declsChanged = true
				}
			}
			if declsChanged {
				if encoded, ok := encodeRawJSON(declsRaw, decls); ok {
					tool[declKey] = encoded
					toolChanged = true
				}
			}
		}
		if toolChanged {
			tools[i] = tool
			changed = true
		}
	}
	if !changed {
		return raw, false
	}
	return encodeRawJSON(raw, tools)
}

func cleanGenerationConfig(raw json.RawMessage) (json.RawMessage, bool) {
	var gen map[string]json.RawMessage
	if json.Unmarshal(raw, &gen) != nil || gen == nil {
		return raw, false
	}
	changed := false
	for _, k := range []string{"responseJsonSchema", "response_json_schema"} {
		if v, ok := gen[k]; ok && !isNullRaw(v) {
			if cleaned, c := cleanSchemaBytes(v, schemaOpts{isToolParams: false, isOpenAPI: false}); c {
				gen[k] = cleaned
				changed = true
			}
		}
	}
	for _, k := range []string{"responseSchema", "response_schema"} {
		if v, ok := gen[k]; ok && !isNullRaw(v) {
			if cleaned, c := cleanSchemaBytes(v, schemaOpts{isToolParams: false, isOpenAPI: true}); c {
				gen[k] = cleaned
				changed = true
			}
		}
	}
	if !changed {
		return raw, false
	}
	return encodeRawJSON(raw, gen)
}

func rewriteToolConfigNames(raw json.RawMessage, toUpstream map[string]string) (json.RawMessage, bool) {
	var tc map[string]json.RawMessage
	if json.Unmarshal(raw, &tc) != nil || tc == nil {
		return raw, false
	}
	changed := false
	for _, fcKey := range []string{"functionCallingConfig", "function_calling_config"} {
		fcRaw, ok := tc[fcKey]
		if !ok {
			continue
		}
		var fc map[string]json.RawMessage
		if json.Unmarshal(fcRaw, &fc) != nil || fc == nil {
			continue
		}
		fcChanged := false
		for _, allowKey := range []string{"allowedFunctionNames", "allowed_function_names"} {
			var names []string
			if json.Unmarshal(fc[allowKey], &names) != nil {
				continue
			}
			namesChanged := false
			for i, name := range names {
				if mapped, ok := toUpstream[name]; ok && mapped != name {
					names[i] = mapped
					namesChanged = true
				}
			}
			if namesChanged {
				if encoded, ok := encodeRawJSON(fc[allowKey], names); ok {
					fc[allowKey] = encoded
					fcChanged = true
				}
			}
		}
		if fcChanged {
			if encoded, ok := encodeRawJSON(fcRaw, fc); ok {
				tc[fcKey] = encoded
				changed = true
			}
		}
	}
	if !changed {
		return raw, false
	}
	return encodeRawJSON(raw, tc)
}

func rewriteContentsFunctionNames(raw json.RawMessage, toUpstream map[string]string) (json.RawMessage, bool) {
	return editArray(raw, func(turn json.RawMessage) (json.RawMessage, bool) {
		return editObject(turn, "parts", func(parts json.RawMessage) (json.RawMessage, bool) {
			return editArray(parts, func(part json.RawMessage) (json.RawMessage, bool) {
				changed := false
				for _, fnKey := range []string{"functionCall", "function_call", "functionResponse", "function_response"} {
					p, ok := editObject(part, fnKey, func(fn json.RawMessage) (json.RawMessage, bool) {
						return editObject(fn, "name", func(nameRaw json.RawMessage) (json.RawMessage, bool) {
							var name string
							if json.Unmarshal(nameRaw, &name) != nil {
								return nameRaw, false
							}
							if mapped, ok := toUpstream[name]; ok && mapped != name {
								return encodeRawJSON(nameRaw, mapped)
							}
							return nameRaw, false
						})
					})
					if ok {
						part = p
						changed = true
					}
				}
				return part, changed
			})
		})
	})
}

// restoreResponseRaw rewrites any sanitized functionCall.name inside a raw GenerateContentResponse
// chunk back to the client's original tool name.
func restoreResponseRaw(data []byte, restore map[string]string) []byte {
	if len(restore) == 0 || !bytes.Contains(data, []byte("functionCall")) {
		return data
	}
	out, changed := editObject(data, "candidates", func(cands json.RawMessage) (json.RawMessage, bool) {
		return editArray(cands, func(cand json.RawMessage) (json.RawMessage, bool) {
			return editObject(cand, "content", func(content json.RawMessage) (json.RawMessage, bool) {
				return editObject(content, "parts", func(parts json.RawMessage) (json.RawMessage, bool) {
					return editArray(parts, func(part json.RawMessage) (json.RawMessage, bool) {
						return editObject(part, "functionCall", func(fn json.RawMessage) (json.RawMessage, bool) {
							return editObject(fn, "name", func(nameRaw json.RawMessage) (json.RawMessage, bool) {
								var name string
								if json.Unmarshal(nameRaw, &name) != nil {
									return nameRaw, false
								}
								if orig, ok := restore[name]; ok && orig != name {
									return encodeRawJSON(nameRaw, orig)
								}
								return nameRaw, false
							})
						})
					})
				})
			})
		})
	})
	if !changed {
		return data
	}
	return out
}

func isNullRaw(raw json.RawMessage) bool {
	s := bytes.TrimSpace(raw)
	return len(s) == 0 || bytes.Equal(s, []byte("null"))
}

func encodeRawJSON(orig json.RawMessage, v any) (json.RawMessage, bool) {
	return encodeJSON(orig, v)
}

func decodeSchemaValue(raw json.RawMessage) (any, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

func cleanSchemaBytes(raw json.RawMessage, opts schemaOpts) (json.RawMessage, bool) {
	orig, ok := decodeSchemaValue(raw)
	if !ok {
		return raw, false
	}
	cleaned := cleanSchemaNode(orig, opts)
	if schemasEqual(orig, cleaned) {
		return raw, false
	}
	out, ok := encodeRawJSON(raw, cleaned)
	if !ok {
		return raw, false
	}
	return out, true
}

func cleanSchemaNode(orig any, opts schemaOpts) any {
	if _, isBool := orig.(bool); isBool {
		if opts.isToolParams {
			return map[string]any{"type": "object", "properties": map[string]any{}}
		}
		return map[string]any{}
	}
	rootMap, ok := orig.(map[string]any)
	if !ok {
		return orig
	}

	step1 := expandAndClean(rootMap, rootMap, opts, map[string]bool{}, 0)
	step2 := mergeConditionals(step1, opts)
	step3 := mergeAllOf(step2, opts)
	step4 := normalizeTypesAndUnions(step3, opts)
	if opts.isToolParams {
		step4 = flattenRootToolUnion(step4)
	}
	step5 := ensureConsistency(step4, opts)
	if opts.isToolParams {
		if m, ok := step5.(map[string]any); ok {
			if s, ok := m["type"].(string); !ok || !strings.EqualFold(s, "object") {
				m["type"] = "object"
			}
		}
	}
	return step5
}

func expandAndClean(v any, doc map[string]any, opts schemaOpts, active map[string]bool, depth int) any {
	switch x := v.(type) {
	case bool:
		return map[string]any{}
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = expandAndClean(item, doc, opts, active, depth)
		}
		return out
	case map[string]any:
		return expandAndCleanObject(x, doc, opts, active, depth)
	default:
		return v
	}
}

func expandAndCleanObject(x map[string]any, doc map[string]any, opts schemaOpts, active map[string]bool, depth int) map[string]any {
	cur := x
	localActive := maps.Clone(active)
	for refIter := depth; ; refIter++ {
		ref, hasRef := cur["$ref"].(string)
		if !hasRef {
			break
		}
		if refIter >= maxRefDepth || localActive[ref] {
			cur = shallowCopyExcept(cur, "$ref")
			if len(cur) == 0 {
				return map[string]any{"type": "object"}
			}
			break
		}
		target, found := resolveJSONPointer(doc, ref)
		if !found {
			cur = shallowCopyExcept(cur, "$ref")
			if len(cur) == 0 {
				return map[string]any{"type": "object"}
			}
			break
		}
		localActive[ref] = true
		var merged map[string]any
		switch tv := target.(type) {
		case map[string]any:
			merged, _ = deepCopy(tv).(map[string]any)
			if merged == nil {
				merged = map[string]any{}
			}
		case bool:
			merged = map[string]any{}
		default:
			merged = map[string]any{"type": "object"}
		}
		for k, val := range cur {
			if k == "$ref" {
				continue
			}
			merged[k] = val
		}
		cur = merged
		depth = refIter + 1
	}

	if isBarePropertyMap(cur) {
		props := map[string]any{}
		for _, k := range slices.Sorted(maps.Keys(cur)) {
			props[k] = cur[k]
		}
		cur = map[string]any{
			"type":       "object",
			"properties": props,
		}
	}

	out := map[string]any{}
	var promotedRequired []string

	for _, k := range slices.Sorted(maps.Keys(cur)) {
		val := cur[k]
		switch {
		case removedKeywords[k] || strings.HasPrefix(k, "x-"):
			continue
		case literalKeywords[k]:
			if opts.isOpenAPI && k == "examples" {
				continue
			}
			out[k] = deepCopy(val)
		case k == "properties":
			if props, ok := val.(map[string]any); ok {
				cleanedProps := map[string]any{}
				for _, propName := range slices.Sorted(maps.Keys(props)) {
					pv := props[propName]
					if pb, isBool := pv.(bool); isBool {
						_ = pb
						cleanedProps[propName] = map[string]any{}
						continue
					}
					if pm, isMap := pv.(map[string]any); isMap {
						if reqBool, ok := pm["required"].(bool); ok {
							if reqBool {
								promotedRequired = append(promotedRequired, propName)
							}
							pm = shallowCopyExcept(pm, "required")
						}
						cleanedProps[propName] = expandAndCleanObject(pm, doc, opts, localActive, depth)
						continue
					}
					cleanedProps[propName] = expandAndClean(pv, doc, opts, localActive, depth)
				}
				out[k] = cleanedProps
			}
		case k == "additionalProperties":
			if b, ok := val.(bool); ok {
				if opts.isOpenAPI {
					if !b {
						addHint(out, "No extra properties allowed")
					}
					continue
				}
				out[k] = b
				continue
			}
			if apMap, ok := val.(map[string]any); ok {
				if opts.isOpenAPI {
					continue
				}
				out[k] = expandAndCleanObject(apMap, doc, opts, localActive, depth)
			}
		case k == "items":
			switch iv := val.(type) {
			case bool:
				out[k] = map[string]any{}
			case map[string]any:
				out[k] = expandAndCleanObject(iv, doc, opts, localActive, depth)
			case []any:
				tuple := make([]any, len(iv))
				for i, elem := range iv {
					tuple[i] = expandAndClean(elem, doc, opts, localActive, depth)
				}
				out[k] = tuple
			default:
				out[k] = deepCopy(val)
			}
		case k == "prefixItems":
			if opts.isOpenAPI {
				continue
			}
			if list, ok := val.([]any); ok {
				cleaned := make([]any, len(list))
				for i, elem := range list {
					cleaned[i] = expandAndClean(elem, doc, opts, localActive, depth)
				}
				out[k] = cleaned
			}
		case k == "anyOf" || k == "oneOf" || k == "allOf":
			if list, ok := val.([]any); ok {
				cleaned := make([]any, 0, len(list))
				for _, elem := range list {
					if b, ok := elem.(bool); ok {
						if !b {
							continue
						}
						cleaned = append(cleaned, map[string]any{})
						continue
					}
					if em, ok := elem.(map[string]any); ok {
						cleaned = append(cleaned, expandAndCleanObject(em, doc, opts, localActive, depth))
					}
				}
				out[k] = cleaned
			}
		case k == "if" || k == "then" || k == "else":
			if sm, ok := val.(map[string]any); ok {
				out[k] = expandAndCleanObject(sm, doc, opts, localActive, depth)
			}
		case integerConstraintKeywords[k]:
			out[k] = normalizeNonNegativeIntString(val)
		default:
			out[k] = deepCopy(val)
		}
	}

	if opts.isOpenAPI {
		for _, ck := range openAPIHintedConstraints {
			if val, exists := cur[ck]; exists {
				addHint(out, ck+": "+valueString(val))
				delete(out, ck)
			}
		}
	}

	if len(promotedRequired) > 0 {
		out["required"] = appendUniqueStrings(out["required"], promotedRequired)
	}

	normalizeNullableKeyword(out, opts)
	normalizeConstAndEnum(out, opts)

	return out
}

func resolveJSONPointer(doc map[string]any, ref string) (any, bool) {
	if doc == nil || !strings.HasPrefix(ref, "#") {
		return nil, false
	}
	if ref == "#" {
		return doc, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	parts := strings.Split(ref[2:], "/")
	var cur any = doc
	for _, rawSeg := range parts {
		seg := unescapeJSONPointerSegment(rawSeg)
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

func unescapeJSONPointerSegment(seg string) string {
	seg = strings.ReplaceAll(seg, "~1", "/")
	seg = strings.ReplaceAll(seg, "~0", "~")
	return seg
}

func isBarePropertyMap(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	hasSchemaChild := false
	for k, v := range m {
		if schemaKeywords[k] {
			return false
		}
		cm, ok := v.(map[string]any)
		if !ok || !looksLikeSchemaMap(cm) {
			return false
		}
		hasSchemaChild = true
	}
	return hasSchemaChild
}

func looksLikeSchemaMap(m map[string]any) bool {
	for k := range m {
		switch k {
		case "type", "properties", "items", "prefixItems", "description", "enum", "const", "anyOf", "oneOf", "allOf", "$ref":
			return true
		}
	}
	return false
}

func normalizeNonNegativeIntString(v any) any {
	s, ok := v.(string)
	if !ok {
		return deepCopy(v)
	}
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return v
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || n < 0 {
		return v
	}
	return json.Number(strconv.FormatInt(n, 10))
}

func normalizeNullableKeyword(m map[string]any, opts schemaOpts) {
	if opts.isOpenAPI {
		return
	}
	raw, exists := m["nullable"]
	if !exists {
		return
	}
	delete(m, "nullable")
	if b, ok := raw.(bool); ok && b {
		markNodeNullable(m, false)
	}
}

func markNodeNullable(m map[string]any, isOpenAPI bool) {
	if isOpenAPI {
		m["nullable"] = true
		return
	}
	switch t := m["type"].(type) {
	case string:
		if strings.EqualFold(t, "null") || t == "" {
			return
		}
		m["type"] = []any{t, "null"}
	case []any:
		for _, elem := range t {
			if s, ok := elem.(string); ok && strings.EqualFold(s, "null") {
				return
			}
		}
		if len(t) > 0 {
			m["type"] = append(slices.Clone(t), "null")
		}
	default:
		if branches, ok := m["anyOf"].([]any); ok {
			for _, b := range branches {
				if bm, ok := b.(map[string]any); ok && isNullBranch(bm) {
					return
				}
			}
			m["anyOf"] = append(slices.Clone(branches), map[string]any{"type": "null"})
		} else if branches, ok := m["oneOf"].([]any); ok {
			for _, b := range branches {
				if bm, ok := b.(map[string]any); ok && isNullBranch(bm) {
					return
				}
			}
			m["oneOf"] = append(slices.Clone(branches), map[string]any{"type": "null"})
		}
	}
}

func normalizeConstAndEnum(m map[string]any, opts schemaOpts) {
	hasNull := false
	if c, hasConst := m["const"]; hasConst {
		delete(m, "const")
		if c == nil {
			hasNull = true
			if _, hasType := m["type"]; !hasType && !opts.isOpenAPI {
				m["type"] = "null"
			}
		} else if _, hasEnum := m["enum"]; !hasEnum {
			m["enum"] = []any{deepCopy(c)}
		}
	}

	if rawEnum, hasEnum := m["enum"]; hasEnum {
		list, ok := rawEnum.([]any)
		if !ok {
			delete(m, "enum")
		} else {
			filtered := make([]any, 0, len(list))
			seen := map[string]bool{}
			allBool := len(list) > 0
			allScalar := true
			allString := true
			for _, v := range list {
				if v == nil {
					hasNull = true
					continue
				}
				key := canonicalJSON(v)
				if seen[key] {
					continue
				}
				seen[key] = true
				filtered = append(filtered, v)
				switch tv := v.(type) {
				case string:
					allBool = false
				case bool:
					_ = tv
					allString = false
				case float64, int, int64, json.Number:
					allBool = false
					allString = false
				default:
					allBool = false
					allString = false
					allScalar = false
				}
			}

			switch {
			case len(filtered) == 0:
				delete(m, "enum")
			case hasBooleanType(m["type"]) && allBool:
				delete(m, "enum")
				if len(filtered) == 1 {
					addHint(m, "Allowed: "+valueString(filtered[0]))
				}
			case allString:
				m["enum"] = filtered
				if isTypeUnset(m["type"]) {
					m["type"] = "string"
				}
			case allScalar:
				strs := make([]any, len(filtered))
				for i, v := range filtered {
					strs[i] = valueString(v)
				}
				m["enum"] = strs
				m["type"] = "string"
			default:
				delete(m, "enum")
			}
		}
	}

	if hasNull {
		markNodeNullable(m, opts.isOpenAPI)
	}
}

func walkSchema(v any, opts schemaOpts, fn func(map[string]any) any) any {
	switch x := v.(type) {
	case []any:
		for i, val := range x {
			x[i] = walkSchema(val, opts, fn)
		}
		return x
	case map[string]any:
		for k, val := range x {
			switch {
			case literalKeywords[k]:
				continue
			case k == "properties":
				if props, ok := val.(map[string]any); ok {
					for name, p := range props {
						props[name] = walkSchema(p, opts, fn)
					}
				}
			case k == "additionalProperties":
				if apMap, ok := val.(map[string]any); ok {
					x[k] = walkSchema(apMap, opts, fn)
				}
			case k == "items" || k == "prefixItems" || k == "anyOf" || k == "oneOf" || k == "allOf" || k == "if" || k == "then" || k == "else":
				x[k] = walkSchema(val, opts, fn)
			}
		}
		return fn(x)
	}
	return v
}

func mergeConditionals(v any, opts schemaOpts) any {
	return walkSchema(v, opts, func(m map[string]any) any {
		for _, k := range []string{"then", "else"} {
			branch, _ := m[k].(map[string]any)
			props, _ := branch["properties"].(map[string]any)
			if len(props) == 0 {
				continue
			}
			dest, ok := m["properties"].(map[string]any)
			if !ok {
				if _, has := m["properties"]; has {
					continue
				}
				dest = map[string]any{}
			}
			for name, p := range props {
				if _, exists := dest[name]; !exists {
					dest[name] = p
				}
			}
			m["properties"] = dest
		}
		delete(m, "if")
		delete(m, "then")
		delete(m, "else")
		return m
	})
}

func mergeAllOf(v any, opts schemaOpts) any {
	return walkSchema(v, opts, func(m map[string]any) any {
		branches, has := m["allOf"]
		if !has {
			return m
		}
		delete(m, "allOf")
		list, _ := branches.([]any)
		for _, b := range list {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			for _, k := range slices.Sorted(maps.Keys(bm)) {
				switch k {
				case "required":
					names, ok := bm[k].([]any)
					if !ok {
						continue
					}
					req, _ := m["required"].([]any)
					for _, n := range names {
						if !slices.Contains(req, n) {
							req = append(req, n)
						}
					}
					if len(req) > 0 {
						m["required"] = req
					}
				case "allOf":
				default:
					fillMissing(m, k, bm[k])
				}
			}
		}
		return m
	})
}

func fillMissing(m map[string]any, k string, v any) {
	cur, has := m[k]
	if !has {
		m[k] = deepCopy(v)
		return
	}
	cm, ok1 := cur.(map[string]any)
	vm, ok2 := v.(map[string]any)
	if !ok1 || !ok2 {
		return
	}
	for kk, vv := range vm {
		fillMissing(cm, kk, vv)
	}
}

func normalizeTypesAndUnions(v any, opts schemaOpts) any {
	return walkSchema(v, opts, func(m map[string]any) any {
		// Tuple items: []any -> prefixItems + items: {} on JSON Schema, or collapsed on OpenAPI.
		if tuple, ok := m["items"].([]any); ok {
			if opts.isOpenAPI {
				var nonNull []any
				for _, elem := range tuple {
					if em, ok := elem.(map[string]any); ok && !isNullBranch(em) {
						nonNull = append(nonNull, em)
					}
				}
				switch len(nonNull) {
				case 0:
					m["items"] = map[string]any{"type": "string"}
				case 1:
					m["items"] = nonNull[0]
				default:
					m["items"] = map[string]any{"anyOf": nonNull}
				}
			} else {
				if _, hasPrefix := m["prefixItems"]; !hasPrefix {
					m["prefixItems"] = tuple
				}
				m["items"] = map[string]any{}
			}
			if isTypeUnset(m["type"]) {
				m["type"] = "array"
			}
		}

		// Type lists: ["string"], ["string", "null"], ["string", "integer"], etc.
		if types, ok := m["type"].([]any); ok {
			normalizeTypeArray(m, types, opts)
		}

		// Normalize oneOf -> anyOf.
		if oneOf, hasOne := m["oneOf"]; hasOne {
			delete(m, "oneOf")
			if _, hasAny := m["anyOf"]; !hasAny {
				m["anyOf"] = oneOf
			}
		}

		// Flatten/clean nested anyOf (skip root when isToolParams — handled in flattenRootToolUnion
		// so root objects don't collapse a single non-object branch blindly).
		if branches, ok := m["anyOf"].([]any); ok {
			simplifyAnyOf(m, branches, opts)
		}

		return m
	})
}

func normalizeTypeArray(m map[string]any, types []any, opts schemaOpts) {
	var nonNull []string
	hasNull := false
	seen := map[string]bool{}
	for _, t := range types {
		s, ok := t.(string)
		if !ok || s == "" {
			continue
		}
		if strings.EqualFold(s, "null") {
			hasNull = true
			continue
		}
		low := strings.ToLower(s)
		if !seen[low] {
			seen[low] = true
			nonNull = append(nonNull, s)
		}
	}

	switch {
	case len(nonNull) == 0:
		if hasNull && !opts.isOpenAPI {
			m["type"] = "null"
		} else {
			delete(m, "type")
			if hasNull {
				m["nullable"] = true
			}
		}
	case len(nonNull) == 1:
		if opts.isOpenAPI {
			m["type"] = nonNull[0]
			if hasNull {
				m["nullable"] = true
			}
		} else if hasNull {
			m["type"] = []any{nonNull[0], "null"}
		} else {
			m["type"] = nonNull[0]
		}
	default:
		delete(m, "type")
		if _, hasAny := m["anyOf"]; hasAny {
			if hasNull {
				markNodeNullable(m, opts.isOpenAPI)
			}
			return
		}
		branches := make([]any, 0, len(nonNull)+1)
		for _, t := range nonNull {
			branch := map[string]any{"type": t}
			if strings.EqualFold(t, "array") {
				if it, hasItems := m["items"]; hasItems {
					branch["items"] = it
					delete(m, "items")
				}
				if pi, hasPrefix := m["prefixItems"]; hasPrefix {
					branch["prefixItems"] = pi
					delete(m, "prefixItems")
				}
			}
			if strings.EqualFold(t, "object") {
				if pr, hasProps := m["properties"]; hasProps {
					branch["properties"] = pr
					delete(m, "properties")
				}
				if req, hasReq := m["required"]; hasReq {
					branch["required"] = req
					delete(m, "required")
				}
			}
			branches = append(branches, branch)
		}
		if hasNull {
			if opts.isOpenAPI {
				m["nullable"] = true
			} else {
				branches = append(branches, map[string]any{"type": "null"})
			}
		}
		m["anyOf"] = branches
	}
}

func simplifyAnyOf(m map[string]any, branches []any, opts schemaOpts) {
	parentProps, _ := m["properties"].(map[string]any)
	var nonNull []map[string]any
	hasNull := false

	var collect func(list []any)
	collect = func(list []any) {
		for _, b := range list {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			if isNullBranch(bm) {
				hasNull = true
				continue
			}
			if isRequiredOnlyBranch(bm) {
				if mat, ok := materializeRequiredBranch(bm, parentProps); ok {
					nonNull = append(nonNull, mat)
				}
				continue
			}
			if len(bm) == 1 {
				if sub, ok := bm["anyOf"].([]any); ok {
					collect(sub)
					continue
				}
			}
			nonNull = append(nonNull, bm)
		}
	}
	collect(branches)

	switch len(nonNull) {
	case 0:
		delete(m, "anyOf")
		if hasNull {
			if opts.isOpenAPI {
				m["nullable"] = true
			} else {
				m["type"] = "null"
			}
		}
	case 1:
		branch := nonNull[0]
		delete(m, "anyOf")
		mergeSingleUnionBranch(m, branch)
		if hasNull {
			markNodeNullable(m, opts.isOpenAPI)
		}
	default:
		out := make([]any, 0, len(nonNull)+1)
		for _, b := range nonNull {
			out = append(out, b)
		}
		if hasNull {
			if opts.isOpenAPI {
				m["nullable"] = true
			} else {
				out = append(out, map[string]any{"type": "null"})
			}
		}
		m["anyOf"] = out
		// Avoid carrying an empty wrapper {"type":"object","properties":{}} alongside a
		// multi-branch anyOf, which Vertex/Gemini can misread as both an object and a union.
		if props, ok := m["properties"].(map[string]any); ok && len(props) == 0 {
			if req, _ := m["required"].([]any); len(req) == 0 {
				delete(m, "properties")
				if s, ok := m["type"].(string); ok && strings.EqualFold(s, "object") {
					delete(m, "type")
				}
			}
		}
	}
}

func mergeSingleUnionBranch(parent, branch map[string]any) {
	for k, v := range branch {
		switch k {
		case "description":
			pDesc, _ := parent["description"].(string)
			bDesc, _ := v.(string)
			switch {
			case pDesc == "" && bDesc != "":
				parent["description"] = bDesc
			case pDesc != "" && bDesc != "" && pDesc != bDesc:
				parent["description"] = pDesc + " (" + bDesc + ")"
			}
		case "properties":
			if bp, ok := v.(map[string]any); ok {
				if pp, ok := parent["properties"].(map[string]any); ok {
					for pk, pv := range bp {
						if _, exists := pp[pk]; !exists {
							pp[pk] = pv
						}
					}
				} else {
					parent["properties"] = bp
				}
			}
		case "required":
			if br, ok := v.([]any); ok {
				var add []string
				for _, r := range br {
					if s, ok := r.(string); ok {
						add = append(add, s)
					}
				}
				parent["required"] = appendUniqueStrings(parent["required"], add)
			}
		default:
			if _, exists := parent[k]; !exists {
				parent[k] = v
			}
		}
	}
}

func isNullBranch(m map[string]any) bool {
	if m["properties"] != nil || m["items"] != nil || m["anyOf"] != nil || m["oneOf"] != nil {
		return false
	}
	switch t := m["type"].(type) {
	case string:
		return strings.EqualFold(t, "null")
	case []any:
		if len(t) == 0 {
			return false
		}
		for _, elem := range t {
			s, ok := elem.(string)
			if !ok || !strings.EqualFold(s, "null") {
				return false
			}
		}
		return true
	}
	return false
}

func isRequiredOnlyBranch(m map[string]any) bool {
	if _, hasReq := m["required"]; !hasReq {
		return false
	}
	for _, k := range []string{"type", "properties", "items", "enum", "const", "anyOf", "oneOf", "allOf"} {
		if _, has := m[k]; has {
			return false
		}
	}
	return true
}

func materializeRequiredBranch(branch, parentProps map[string]any) (map[string]any, bool) {
	if len(parentProps) == 0 {
		return nil, false
	}
	reqs, ok := branch["required"].([]any)
	if !ok || len(reqs) == 0 {
		return nil, false
	}
	props := map[string]any{}
	var keptReq []any
	for _, r := range reqs {
		name, ok := r.(string)
		if !ok {
			continue
		}
		pv, exists := parentProps[name]
		if !exists {
			return nil, false
		}
		props[name] = deepCopy(pv)
		keptReq = append(keptReq, name)
	}
	if len(keptReq) == 0 {
		return nil, false
	}
	out := maps.Clone(branch)
	out["type"] = "object"
	out["properties"] = props
	out["required"] = keptReq
	return out, true
}

func flattenRootToolUnion(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	var branches []any
	for _, k := range []string{"anyOf", "oneOf"} {
		if list, ok := m[k].([]any); ok {
			branches = append(branches, list...)
		}
		delete(m, k)
	}
	if len(branches) == 0 {
		return m
	}

	props, _ := m["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}

	var objBranches []map[string]any
	var collect func(list []any)
	collect = func(list []any) {
		for _, b := range list {
			bm, ok := b.(map[string]any)
			if !ok || isNullBranch(bm) {
				continue
			}
			if sub, ok := bm["anyOf"].([]any); ok && bm["properties"] == nil {
				collect(sub)
				continue
			}
			if canBeObjectSchema(bm) {
				objBranches = append(objBranches, bm)
			}
		}
	}
	collect(branches)

	for _, bm := range objBranches {
		bp, _ := bm["properties"].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(bp)) {
			if _, exists := props[name]; !exists {
				props[name] = bp[name]
			}
		}
	}

	// Required properties common to every non-null object branch stay required alongside root's own.
	if len(objBranches) > 0 {
		common := requiredSet(objBranches[0]["required"])
		for _, bm := range objBranches[1:] {
			next := requiredSet(bm["required"])
			for name := range common {
				if !next[name] {
					delete(common, name)
				}
			}
		}
		if len(common) > 0 {
			var ordered []string
			if firstReq, ok := objBranches[0]["required"].([]any); ok {
				for _, r := range firstReq {
					if s, ok := r.(string); ok && common[s] {
						ordered = append(ordered, s)
					}
				}
			}
			if len(ordered) > 0 {
				m["required"] = appendUniqueStrings(m["required"], ordered)
			}
		}
	}

	m["type"] = "object"
	if len(props) > 0 {
		m["properties"] = props
	}
	return m
}

func canBeObjectSchema(m map[string]any) bool {
	if m["properties"] != nil {
		return true
	}
	switch t := m["type"].(type) {
	case nil:
		return m["items"] == nil && m["enum"] == nil
	case string:
		return t == "" || strings.EqualFold(t, "object")
	case []any:
		for _, elem := range t {
			if s, ok := elem.(string); ok && strings.EqualFold(s, "object") {
				return true
			}
		}
		return false
	}
	return false
}

func requiredSet(v any) map[string]bool {
	set := map[string]bool{}
	list, ok := v.([]any)
	if !ok {
		return set
	}
	for _, r := range list {
		if s, ok := r.(string); ok && s != "" {
			set[s] = true
		}
	}
	return set
}

func ensureConsistency(v any, opts schemaOpts) any {
	return walkSchema(v, opts, func(m map[string]any) any {
		// 1. properties & required consistency.
		if rawProps, hasProps := m["properties"]; hasProps {
			props, isMap := rawProps.(map[string]any)
			if !isMap {
				delete(m, "properties")
				delete(m, "required")
			} else {
				if !typeIncludes(m["type"], "object") {
					m["type"] = "object"
				}
				cleanRequired(m, props)
			}
		} else {
			delete(m, "required")
		}

		// 2. Array items consistency.
		_, hasItems := m["items"]
		if hasItems {
			if isTypeUnset(m["type"]) {
				m["type"] = "array"
			} else if !typeAllows(m["type"], "array") {
				delete(m, "items")
				delete(m, "prefixItems")
			}
		} else if typeIncludes(m["type"], "array") {
			if _, hasPrefix := m["prefixItems"]; hasPrefix && !opts.isOpenAPI {
				m["items"] = map[string]any{}
			} else {
				m["items"] = map[string]any{"type": "string"}
			}
		}

		return m
	})
}

func cleanRequired(m map[string]any, props map[string]any) {
	rawReq, hasReq := m["required"]
	if !hasReq {
		return
	}
	list, ok := rawReq.([]any)
	if !ok {
		delete(m, "required")
		return
	}
	kept := make([]any, 0, len(list))
	seen := map[string]bool{}
	for _, item := range list {
		name, ok := item.(string)
		if !ok || seen[name] {
			continue
		}
		if _, exists := props[name]; !exists {
			continue
		}
		seen[name] = true
		kept = append(kept, name)
	}
	if len(kept) == 0 {
		delete(m, "required")
		return
	}
	m["required"] = kept
}

func isTypeUnset(t any) bool {
	if t == nil {
		return true
	}
	if s, ok := t.(string); ok {
		return s == ""
	}
	if list, ok := t.([]any); ok {
		return len(list) == 0
	}
	return true
}

func hasBooleanType(t any) bool {
	return typeIncludes(t, "boolean")
}

func typeIncludes(t any, target string) bool {
	switch tv := t.(type) {
	case string:
		return strings.EqualFold(tv, target)
	case []any:
		for _, elem := range tv {
			if s, ok := elem.(string); ok && strings.EqualFold(s, target) {
				return true
			}
		}
	}
	return false
}

func typeAllows(t any, target string) bool {
	return isTypeUnset(t) || typeIncludes(t, target)
}

func appendUniqueStrings(existing any, extra []string) []any {
	out := make([]any, 0, len(extra))
	seen := map[string]bool{}
	if list, ok := existing.([]any); ok {
		for _, item := range list {
			if s, ok := item.(string); ok && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	for _, s := range extra {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

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

func valueString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if n, ok := v.(json.Number); ok {
		return n.String()
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func canonicalJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return buf.String()
}

func shallowCopyExcept(m map[string]any, skip string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != skip {
			out[k] = v
		}
	}
	return out
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopy(val)
		}
		return out
	default:
		return v
	}
}

func schemasEqual(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case json.Number:
		bv, ok := b.(json.Number)
		return ok && av.String() == bv.String()
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !schemasEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			ov, exists := bv[k]
			if !exists || !schemasEqual(v, ov) {
				return false
			}
		}
		return true
	default:
		return canonicalJSON(a) == canonicalJSON(b)
	}
}
