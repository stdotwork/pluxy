package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Tool parameter schema cleaner for Gemini API and Vertex AI function declarations
// (parametersJsonSchema / parameters_json_schema / parameters, including inside a countTokens
// generateContentRequest wrapper). Keeps wire field names and the case of the types a client
// wrote (such as uppercase OpenAPI ones); the types it adds or replaces itself ("object",
// "array", "string", "integer", "number", "null") are lowercase.

const (
	// maxRefDepth bounds a chain of $refs, so a schema that refers to itself ends.
	maxRefDepth = 16
	// maxRefNodes bounds the JSON values that $ref expansion produces for one request, all its
	// tools together. Depth alone does not bound the tree: a def with four refs to the next def
	// multiplies the output by four at every level.
	maxRefNodes = 50000
)

// refBudget is what is left of a request's maxRefNodes.
type refBudget struct{ left int }

func newRefBudget() *refBudget { return &refBudget{left: maxRefNodes} }

var (
	removedKeywords = map[string]bool{
		"$schema": true, "$defs": true, "definitions": true, "$id": true, "id": true,
		"$anchor": true, "$vocabulary": true, "$dynamicRef": true, "$dynamicAnchor": true, "$comment": true,
		"$ref": true, "propertyNames": true, "patternProperties": true,
		"dependentRequired": true, "dependentSchemas": true, "dependencies": true, "not": true,
		"unevaluatedProperties": true, "unevaluatedItems": true, "additionalItems": true,
		"contentSchema": true, "contentMediaType": true, "contentEncoding": true,
		"readOnly": true, "writeOnly": true, "deprecated": true, "discriminator": true,
		"enumDescriptions": true, "enumTitles": true, "prefill": true, "encrypted": true,
	}
	openAPIHintedConstraints = []string{"exclusiveMinimum", "exclusiveMaximum", "multipleOf", "uniqueItems", "contains", "examples"}
	literalKeywords          = map[string]bool{"default": true, "example": true, "examples": true, "enum": true, "const": true, "required": true, "propertyOrdering": true, "dependentRequired": true}
	intConstraintKeywords    = map[string]bool{"minLength": true, "maxLength": true, "minItems": true, "maxItems": true, "minProperties": true, "maxProperties": true}
	schemaKeywords           = map[string]bool{"type": true, "properties": true, "$ref": true, "anyOf": true, "oneOf": true, "allOf": true, "items": true, "prefixItems": true, "enum": true, "const": true, "additionalProperties": true, "if": true, "then": true, "else": true, "not": true, "$schema": true, "$defs": true, "definitions": true, "title": true, "description": true, "required": true, "nullable": true}
	containerMapKeywords     = map[string]bool{"properties": true, "$defs": true, "definitions": true, "patternProperties": true, "dependentSchemas": true, "dependencies": true}
)

func cleanToolSchemas(body []byte) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil || top == nil {
		return body
	}
	if inner, ok := top["generateContentRequest"]; ok && len(bytes.TrimSpace(inner)) > 0 && string(bytes.TrimSpace(inner)) != "null" {
		cleaned := cleanRequestToolsField(inner)
		if bytes.Equal(cleaned, inner) {
			return body
		}
		top["generateContentRequest"] = cleaned
		if out, err := json.Marshal(top); err == nil {
			return out
		}
		return body
	}
	return cleanRequestToolsField(body)
}

func cleanRequestToolsField(body []byte) []byte {
	budget := newRefBudget()
	var req map[string]json.RawMessage
	var tools []map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil || json.Unmarshal(req["tools"], &tools) != nil {
		return body
	}
	changed := false
	for _, tool := range tools {
		for _, declKey := range []string{"functionDeclarations", "function_declarations"} {
			var decls []map[string]json.RawMessage
			if json.Unmarshal(tool[declKey], &decls) != nil {
				continue
			}
			declsChanged := false
			for _, d := range decls {
				for _, f := range []string{"parametersJsonSchema", "parameters_json_schema"} {
					if cleaned, c := cleanToolSchema(d[f], false, budget); c {
						d[f], declsChanged = cleaned, true
					}
				}
				if cleaned, c := cleanToolSchema(d["parameters"], true, budget); c {
					d["parameters"], declsChanged = cleaned, true
				}
			}
			if declsChanged {
				if encoded, err := json.Marshal(decls); err == nil {
					tool[declKey], changed = encoded, true
				}
			}
		}
	}
	if !changed {
		return body
	}
	if encoded, err := json.Marshal(tools); err == nil {
		req["tools"] = encoded
		if out, err := json.Marshal(req); err == nil {
			return out
		}
	}
	return body
}

// cleanToolSchema cleans one tool parameter schema; openAPI selects OpenAPI Schema mode
// (parameters) vs JSON Schema mode (parametersJsonSchema / parameters_json_schema). budget is the
// request's allowance for $ref expansion, shared by all its tools.
func cleanToolSchema(raw json.RawMessage, openAPI bool, budget *refBudget) (json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil || root == nil {
		return raw, false
	}
	orig := deepClone(root)

	root = normalizeMalformed(root)
	root = resolveRefs(&refExpansion{root: root, active: map[string]bool{}, budget: budget, sizes: map[string]int{}}, root, 0)
	if m, ok := root.(map[string]any); ok {
		root = cleanSchemaNode(m, true, openAPI)
	}
	if reflect.DeepEqual(orig, root) {
		return raw, false
	}
	out, err := json.Marshal(root)
	if err != nil {
		return raw, false
	}
	return out, true
}

func normalizeMalformed(v any) any {
	switch x := v.(type) {
	case bool:
		return map[string]any{}
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = normalizeMalformed(item)
		}
		return out
	case map[string]any:
		if isBarePropertyMap(x) {
			props, req := repairPropertyMap(x)
			out := map[string]any{"type": "object", "properties": props}
			if len(req) > 0 {
				out["required"] = mergeRequired(nil, req)
			}
			return out
		}
		out := make(map[string]any, len(x))
		var promotedReq []string
		for k, val := range x {
			switch {
			case literalKeywords[k]:
				out[k] = val
			case k == "properties":
				if pm, ok := val.(map[string]any); ok {
					out[k], promotedReq = repairPropertyMap(pm)
				} else {
					out[k] = val
				}
			case containerMapKeywords[k]:
				if cm, ok := val.(map[string]any); ok {
					norm := make(map[string]any, len(cm))
					for _, name := range slices.Sorted(maps.Keys(cm)) {
						norm[name] = normalizeMalformed(cm[name])
					}
					out[k] = norm
				} else {
					out[k] = val
				}
			case intConstraintKeywords[k]:
				if s, ok := val.(string); ok {
					if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 && strconv.FormatInt(n, 10) == s {
						out[k] = json.Number(s)
						continue
					}
				}
				out[k] = val
			case k == "additionalProperties":
				if _, ok := val.(bool); ok {
					out[k] = val
				} else {
					out[k] = normalizeMalformed(val)
				}
			case k == "anyOf" || k == "oneOf":
				if arr, ok := val.([]any); ok {
					var kept []any
					for _, b := range arr {
						if bb, ok := b.(bool); ok {
							if bb {
								kept = append(kept, map[string]any{})
							}
							continue
						}
						kept = append(kept, normalizeMalformed(b))
					}
					out[k] = kept
				} else {
					out[k] = val
				}
			case k == "items" || k == "prefixItems" || k == "allOf" || k == "if" || k == "then" || k == "else" || k == "not" || k == "contains":
				out[k] = normalizeMalformed(val)
			default:
				out[k] = val
			}
		}
		if len(promotedReq) > 0 {
			out["required"] = mergeRequired(out["required"], promotedReq)
		}
		return out
	default:
		return v
	}
}

func isBarePropertyMap(m map[string]any) bool {
	if len(m) == 0 {
		return false
	}
	for k := range m {
		if schemaKeywords[k] {
			return false
		}
	}
	for _, v := range m {
		sub, ok := v.(map[string]any)
		if !ok || !slices.ContainsFunc(slices.Collect(maps.Keys(sub)), func(k string) bool { return schemaKeywords[k] }) {
			return false
		}
	}
	return true
}

func repairPropertyMap(m map[string]any) (map[string]any, []string) {
	props := make(map[string]any, len(m))
	var req []string
	for _, name := range slices.Sorted(maps.Keys(m)) {
		sub := normalizeMalformed(m[name])
		if sm, ok := sub.(map[string]any); ok {
			if rb, isBool := sm["required"].(bool); isBool {
				if rb {
					req = append(req, name)
				}
				delete(sm, "required")
			}
		}
		props[name] = sub
	}
	return props, req
}

// refExpansion is the state of expanding the $refs of one schema.
type refExpansion struct {
	root   any
	active map[string]bool // the refs being expanded, so a cycle ends
	budget *refBudget
	sizes  map[string]int // each ref's target size, counted once (up to maxRefNodes+1)
}

// charge takes the size of ref's target from the budget and says whether it fit. A ref that does
// not fit costs nothing, so later, smaller refs can still fit.
func (x *refExpansion) charge(ref string, target any) bool {
	size, ok := x.sizes[ref]
	if !ok {
		left := maxRefNodes + 1
		countNodes(target, &left)
		size = maxRefNodes + 1 - left
		x.sizes[ref] = size
	}
	if size > x.budget.left {
		return false
	}
	x.budget.left -= size
	return true
}

// countNodes subtracts the number of JSON values in v from *left, and stops walking once it is
// used up.
func countNodes(v any, left *int) {
	if *left <= 0 {
		return
	}
	*left--
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			countNodes(item, left)
		}
	case map[string]any:
		for _, item := range x {
			countNodes(item, left)
		}
	}
}

func resolveRefs(x *refExpansion, v any, depth int) any {
	switch n := v.(type) {
	case []any:
		out := make([]any, len(n))
		for i, item := range n {
			out[i] = resolveRefs(x, item, depth)
		}
		return out
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok {
			return resolveRefNode(x, n, ref, depth)
		}
		out := make(map[string]any, len(n))
		// In key order, so that which refs the budget pays for does not depend on map order.
		for _, k := range slices.Sorted(maps.Keys(n)) {
			val := n[k]
			switch {
			case literalKeywords[k]:
				out[k] = deepClone(val)
			case k == "$defs" || k == "definitions":
			case containerMapKeywords[k]:
				if cm, ok := val.(map[string]any); ok {
					rm := make(map[string]any, len(cm))
					for _, name := range slices.Sorted(maps.Keys(cm)) {
						rm[name] = resolveRefs(x, cm[name], depth)
					}
					out[k] = rm
				} else {
					out[k] = deepClone(val)
				}
			default:
				out[k] = resolveRefs(x, val, depth)
			}
		}
		return out
	default:
		return v
	}
}

func resolveRefNode(x *refExpansion, node map[string]any, ref string, depth int) any {
	var target any
	var ok bool
	if strings.HasPrefix(ref, "#/") && !x.active[ref] && depth < maxRefDepth {
		target, ok = resolveJSONPointer(x.root, ref)
		// The target's size is paid before it is cloned. A target that does not fit takes the
		// fallback below, which keeps its type, nullable and description.
		if ok && !x.charge(ref, target) {
			ok = false
		}
	}
	if !ok {
		out := make(map[string]any, len(node)+1)
		if tm, isMap := target.(map[string]any); isMap {
			for _, k := range []string{"type", "nullable", "description"} {
				if val, exists := tm[k]; exists {
					out[k] = deepClone(val)
				}
			}
		}
		for _, k := range slices.Sorted(maps.Keys(node)) {
			if k != "$ref" && k != "$defs" && k != "definitions" {
				out[k] = resolveRefs(x, node[k], depth)
			}
		}
		hintName := ref
		if i := strings.LastIndexByte(ref, '/'); i >= 0 && i+1 < len(ref) {
			hintName = strings.ReplaceAll(strings.ReplaceAll(ref[i+1:], "~1", "/"), "~0", "~")
		}
		addHint(out, "See: "+hintName)
		if out["type"] == nil && out["properties"] == nil && out["items"] == nil && out["anyOf"] == nil && out["oneOf"] == nil && out["allOf"] == nil && out["enum"] == nil {
			out["type"] = "object"
		}
		return out
	}

	x.active[ref] = true
	resolved := resolveRefs(x, deepClone(target), depth+1)
	delete(x.active, ref)

	tm, isMap := resolved.(map[string]any)
	if !isMap {
		return resolved
	}
	out := maps.Clone(tm)
	for _, k := range slices.Sorted(maps.Keys(node)) {
		v := node[k]
		if k == "$ref" || k == "$defs" || k == "definitions" {
			continue
		}
		if literalKeywords[k] {
			out[k] = deepClone(v)
		} else {
			out[k] = resolveRefs(x, v, depth)
		}
	}
	return out
}

func resolveJSONPointer(root any, ref string) (any, bool) {
	cur := root
	for _, rawPart := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(rawPart, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			if cur, _ = node[part]; cur == nil {
				if _, ok := node[part]; !ok {
					return nil, false
				}
			}
		case []any:
			idx, err := strconv.Atoi(part)
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

func cleanSchemaNode(m map[string]any, isToolRoot, openAPI bool) map[string]any {
	out := maps.Clone(m)
	if c, ok := out["const"]; ok {
		if _, has := out["enum"]; !has {
			out["enum"] = []any{c}
		}
		delete(out, "const")
	}

	if pm, ok := out["properties"].(map[string]any); ok {
		cleaned := make(map[string]any, len(pm))
		for name, sub := range pm {
			cleaned[name] = cleanChild(sub, openAPI)
		}
		out["properties"] = cleaned
	}
	for _, k := range []string{"items", "additionalProperties", "then", "else", "prefixItems", "allOf", "anyOf", "oneOf"} {
		if val, has := out[k]; has {
			out[k] = cleanChild(val, openAPI)
		}
	}

	mergeConditionalsAndAllOf(out)
	cleanUnions(out, isToolRoot, openAPI)
	normalizeTypeAndNullable(out, openAPI)
	cleanEnum(out, openAPI)

	for k := range out {
		if removedKeywords[k] || strings.HasPrefix(k, "x-") {
			delete(out, k)
		}
	}
	if openAPI {
		if ap, ok := out["additionalProperties"]; ok {
			if ap == false {
				addHint(out, "No extra properties allowed")
			}
			delete(out, "additionalProperties")
		}
		for _, k := range openAPIHintedConstraints {
			if val, ok := out[k]; ok {
				addHint(out, k+": "+valueString(val))
				delete(out, k)
			}
		}
	}

	enforceObjectAndItems(out, openAPI)
	return out
}

func cleanChild(v any, openAPI bool) any {
	switch x := v.(type) {
	case map[string]any:
		return cleanSchemaNode(x, false, openAPI)
	case []any:
		out := make([]any, len(x))
		for i, sub := range x {
			out[i] = cleanChild(sub, openAPI)
		}
		return out
	default:
		return v
	}
}

func mergeConditionalsAndAllOf(m map[string]any) {
	props, _ := m["properties"].(map[string]any)
	for _, k := range []string{"then", "else"} {
		branch, ok := m[k].(map[string]any)
		delete(m, k)
		if !ok {
			continue
		}
		if bp, ok := branch["properties"].(map[string]any); ok {
			if props == nil && len(bp) > 0 {
				props = make(map[string]any, len(bp))
				m["properties"] = props
			}
			for name, val := range bp {
				if _, exists := props[name]; !exists {
					props[name] = val
				}
			}
		}
	}
	delete(m, "if")

	arr, ok := m["allOf"].([]any)
	if !ok {
		return
	}
	delete(m, "allOf")
	for _, item := range arr {
		bm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if req := stringSlice(bm["required"]); len(req) > 0 {
			m["required"] = mergeRequired(m["required"], req)
		}
		for k, v := range bm {
			if k != "required" && k != "if" && k != "then" && k != "else" && k != "allOf" {
				mergeMissingField(m, k, v)
			}
		}
	}
}

func mergeMissingField(dst map[string]any, k string, v any) {
	existing, exists := dst[k]
	if !exists {
		dst[k] = v
		return
	}
	if dm, ok1 := existing.(map[string]any); ok1 {
		if sm, ok2 := v.(map[string]any); ok2 {
			for sk, sv := range sm {
				mergeMissingField(dm, sk, sv)
			}
		}
	}
}

func cleanUnions(m map[string]any, isToolRoot, openAPI bool) {
	anyArr, hasAny := m["anyOf"].([]any)
	oneArr, hasOne := m["oneOf"].([]any)
	if !hasAny && !hasOne {
		return
	}
	if hasAny && hasOne {
		if openAPI {
			if raw, err := json.Marshal(oneArr); err == nil {
				addHint(m, "oneOf: "+string(raw))
			}
		}
		delete(m, "oneOf")
	}
	key, rawBranches := "anyOf", anyArr
	if !hasAny {
		key, rawBranches = "oneOf", oneArr
	}

	parentProps, _ := m["properties"].(map[string]any)
	var nonNull []map[string]any
	hasNull, _ := m["nullable"].(bool)
	for _, item := range rawBranches {
		bm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if bm["properties"] == nil && bm["items"] == nil && (bm["type"] == "null" || (typeIncludes(bm["type"], "null") && len(stringSlice(bm["type"])) == 1)) {
			hasNull = true
			continue
		}
		if bm["required"] != nil && bm["type"] == nil && bm["properties"] == nil && bm["items"] == nil && bm["enum"] == nil && bm["anyOf"] == nil && bm["oneOf"] == nil && bm["allOf"] == nil {
			if reqs := stringSlice(bm["required"]); len(reqs) > 0 && len(parentProps) > 0 {
				props := make(map[string]any, len(reqs))
				okAll := true
				for _, r := range reqs {
					if pv, exists := parentProps[r]; exists {
						props[r] = pv
					} else {
						okAll = false
						break
					}
				}
				if okAll {
					mat := maps.Clone(bm)
					mat["type"], mat["properties"] = "object", props
					nonNull = append(nonNull, mat)
				}
			}
			continue
		}
		nonNull = append(nonNull, bm)
	}

	if isToolRoot {
		delete(m, key)
		props, _ := m["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
		}
		own := maps.Clone(props)
		branchSchemas := map[string][]any{}
		var commonReq []string
		first := true
		for _, b := range nonNull {
			if bp, ok := b["properties"].(map[string]any); ok {
				for _, k := range slices.Sorted(maps.Keys(bp)) {
					if _, exists := own[k]; !exists {
						branchSchemas[k] = append(branchSchemas[k], bp[k])
					}
				}
			}
			if len(b) == 0 {
				continue
			}
			bReq := stringSlice(b["required"])
			if first {
				commonReq, first = bReq, false
			} else {
				commonReq = slices.DeleteFunc(slices.Clone(commonReq), func(r string) bool { return !slices.Contains(bReq, r) })
			}
		}
		for k, schemas := range branchSchemas {
			props[k] = mergeBranchSchemas(schemas, openAPI)
		}
		if len(props) > 0 || m["type"] == nil {
			m["type"], m["properties"] = "object", props
		}
		if len(commonReq) > 0 {
			m["required"] = mergeRequired(m["required"], commonReq)
		}
		return
	}

	switch len(nonNull) {
	case 0:
		if hasNull {
			delete(m, key)
			if openAPI {
				m["type"], m["nullable"] = "string", true
			} else {
				m["type"] = "null"
			}
		}
	case 1:
		delete(m, key)
		desc, _ := m["description"].(string)
		for bk, bv := range nonNull[0] {
			if bk == "description" {
				if bd, _ := bv.(string); desc != "" && bd != "" && desc != bd {
					m["description"] = desc + " (" + bd + ")"
				} else if bd != "" {
					m["description"] = bd
				}
				continue
			}
			m[bk] = bv
		}
		if hasNull {
			makeNullable(m, openAPI)
		}
	default:
		delete(m, "oneOf")
		branches := make([]any, 0, len(nonNull)+1)
		for _, b := range nonNull {
			branches = append(branches, b)
		}
		if hasNull {
			if openAPI {
				m["nullable"] = true
			} else {
				branches = append(branches, map[string]any{"type": "null"})
			}
		}
		m["anyOf"] = branches
	}
}

// mergeBranchSchemas merges the schemas one property has in the branches of a root union, given in
// branch order. Identical schemas keep one. Schemas that differ only in enum and description, all
// with an enum, become one schema whose enum holds every branch's values (first-seen order, no
// duplicates) and whose description is the first non-empty one. Anything else becomes an anyOf of
// the distinct schemas, which loses nothing, cleaned as a union is (a null branch is nullability).
func mergeBranchSchemas(schemas []any, openAPI bool) any {
	var distinct []any
	for _, s := range schemas {
		if !slices.ContainsFunc(distinct, func(d any) bool { return reflect.DeepEqual(d, s) }) {
			distinct = append(distinct, s)
		}
	}
	if len(distinct) == 1 {
		return distinct[0]
	}
	if merged, ok := mergeEnumSchemas(distinct); ok {
		return merged
	}
	// Cleaned like any other union, so that a null branch becomes nullability and a second
	// cleaning changes nothing.
	return cleanSchemaNode(map[string]any{"anyOf": distinct}, false, openAPI)
}

func mergeEnumSchemas(schemas []any) (map[string]any, bool) {
	var base map[string]any
	var values []any
	seen := map[string]bool{}
	description := ""
	for _, s := range schemas {
		sm, ok := s.(map[string]any)
		if !ok {
			return nil, false
		}
		enum, ok := sm["enum"].([]any)
		if !ok {
			return nil, false
		}
		rest := maps.Clone(sm)
		delete(rest, "enum")
		delete(rest, "description")
		if base == nil {
			base = rest
		} else if !reflect.DeepEqual(base, rest) {
			return nil, false
		}
		for _, v := range enum {
			key, _ := json.Marshal(v)
			if !seen[string(key)] {
				seen[string(key)] = true
				values = append(values, v)
			}
		}
		if d, _ := sm["description"].(string); description == "" {
			description = d
		}
	}
	base["enum"] = values
	if description != "" {
		base["description"] = description
	}
	return base, true
}

func normalizeTypeAndNullable(m map[string]any, openAPI bool) {
	if arr, ok := m["type"].([]any); ok {
		var nonNull []string
		hasNull := false
		for _, v := range arr {
			if s, ok := v.(string); ok {
				if s == "null" {
					hasNull = true
				} else if !slices.Contains(nonNull, s) {
					nonNull = append(nonNull, s)
				}
			}
		}
		switch len(nonNull) {
		case 0:
			if openAPI {
				m["type"], m["nullable"] = "string", true
			} else {
				m["type"] = "null"
			}
		case 1:
			if openAPI {
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
			branches := make([]any, 0, len(nonNull)+1)
			for _, t := range nonNull {
				branches = append(branches, map[string]any{"type": t})
			}
			if hasNull {
				if openAPI {
					m["nullable"] = true
				} else {
					branches = append(branches, map[string]any{"type": "null"})
				}
			}
			m["anyOf"] = branches
		}
	}
	if !openAPI {
		if nb, ok := m["nullable"].(bool); ok {
			delete(m, "nullable")
			if nb {
				makeNullable(m, false)
			}
		}
	}
}

func makeNullable(m map[string]any, openAPI bool) {
	if openAPI {
		m["nullable"] = true
		return
	}
	delete(m, "nullable")
	switch t := m["type"].(type) {
	case string:
		if t != "" && !strings.EqualFold(t, "null") {
			m["type"] = []any{t, "null"}
		}
	case []any:
		if !slices.Contains(t, any("null")) {
			m["type"] = append(t, "null")
		}
	}
}

func cleanEnum(m map[string]any, openAPI bool) {
	arr, ok := m["enum"].([]any)
	if !ok {
		return
	}
	hasNull := false
	var nonNull []any
	for _, v := range arr {
		if v == nil {
			hasNull = true
		} else {
			nonNull = append(nonNull, v)
		}
	}
	if len(nonNull) == 0 {
		delete(m, "enum")
		return
	}
	if t, ok := m["type"].(string); ok && strings.EqualFold(t, "boolean") {
		if len(nonNull) == 1 {
			addHint(m, "Allowed: "+valueString(nonNull[0]))
		}
		delete(m, "enum")
		return
	}
	if !openAPI {
		if numberType, ok := numericEnumType(m["type"], nonNull); ok {
			var numbers []any
			seen := map[string]bool{}
			for _, v := range nonNull {
				if s := valueString(v); !seen[s] {
					seen[s] = true
					numbers = append(numbers, v)
				}
			}
			m["enum"], m["type"] = numbers, numberType
			if hasNull {
				makeNullable(m, false)
			}
			return
		}
	}
	var out []any
	seen := map[string]bool{}
	for _, v := range nonNull {
		if s := valueString(v); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	m["enum"] = out
	if ts, ok := m["type"].(string); !ok || !strings.EqualFold(ts, "string") {
		m["type"] = "string"
	}
	if hasNull {
		makeNullable(m, openAPI)
	}
}

// numericEnumType says whether a JSON Schema mode enum keeps its numbers, and the type it keeps
// them under: every value is a JSON number and the declared type is integer or number (alone or in
// a list with null), or absent, in which case it is integer unless a value has a ".", "e" or "E".
func numericEnumType(declared any, values []any) (any, bool) {
	whole := true
	for _, v := range values {
		n, ok := v.(json.Number)
		if !ok {
			return nil, false
		}
		if strings.ContainsAny(n.String(), ".eE") {
			whole = false
		}
	}
	var base any
	switch t := declared.(type) {
	case nil:
		base = "number"
		if whole {
			base = "integer"
		}
		return base, true
	case string:
		base = t
	case []any:
		var nonNull []any
		for _, x := range t {
			if x != "null" {
				nonNull = append(nonNull, x)
			}
		}
		if len(nonNull) != 1 {
			return nil, false
		}
		base = nonNull[0]
	default:
		return nil, false
	}
	if s, ok := base.(string); ok && (strings.EqualFold(s, "integer") || strings.EqualFold(s, "number")) {
		return declared, true
	}
	return nil, false
}

func enforceObjectAndItems(m map[string]any, openAPI bool) {
	if pm, ok := m["properties"].(map[string]any); ok {
		if !typeIncludes(m["type"], "object") {
			m["type"] = "object"
		}
		if req, has := m["required"]; has {
			var valid []any
			for _, r := range stringSlice(req) {
				if _, ok := pm[r]; ok && !slices.Contains(valid, any(r)) {
					valid = append(valid, r)
				}
			}
			if len(valid) == 0 {
				delete(m, "required")
			} else {
				m["required"] = valid
			}
		}
	} else {
		delete(m, "required")
	}

	if tuple, ok := m["items"].([]any); ok {
		if openAPI {
			switch len(tuple) {
			case 0:
				m["items"] = map[string]any{"type": "string"}
			case 1:
				m["items"] = tuple[0]
			default:
				m["items"] = map[string]any{"anyOf": tuple}
			}
		} else {
			m["prefixItems"], m["items"] = tuple, map[string]any{}
		}
	}
	if openAPI {
		delete(m, "prefixItems")
	}

	_, hasItems := m["items"]
	_, hasPrefix := m["prefixItems"]
	if m["type"] == nil && (hasItems || hasPrefix) {
		m["type"] = "array"
	} else if m["type"] != nil && !typeIncludes(m["type"], "array") && hasItems {
		delete(m, "items")
	} else if typeIncludes(m["type"], "array") && !hasItems && !hasPrefix {
		m["items"] = map[string]any{"type": "string"}
	}
}

func typeIncludes(t any, want string) bool {
	switch x := t.(type) {
	case string:
		return strings.EqualFold(x, want)
	case []any:
		for _, v := range x {
			if s, ok := v.(string); ok && strings.EqualFold(s, want) {
				return true
			}
		}
	}
	return false
}

func addHint(m map[string]any, hint string) {
	if hint == "" {
		return
	}
	if cur, _ := m["description"].(string); cur != "" {
		m["description"] = cur + " (" + hint + ")"
	} else {
		m["description"] = hint
	}
}

func valueString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func stringSlice(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mergeRequired(cur any, extra []string) []any {
	out := make([]any, 0, len(extra))
	for _, s := range append(stringSlice(cur), extra...) {
		if !slices.Contains(out, any(s)) {
			out = append(out, s)
		}
	}
	return out
}

func deepClone(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, elem := range x {
			out[i] = deepClone(elem)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepClone(val)
		}
		return out
	default:
		return v
	}
}
