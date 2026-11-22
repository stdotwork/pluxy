package main

import (
	"bytes"
	"encoding/json"
	"maps"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// The Codex endpoint rejects some JSON Schema forms that clients send in a function tool's
// parameters, mostly tools from MCP servers. A rejected request fails whole, so these rules
// rewrite the schema first. Each rule only removes or replaces something the tool's client
// checks again itself, or states the same constraint in another form:
//
//   - A root `anyOf` or `oneOf` union is flattened into a root `object` schema with the object
//     branches' properties merged and their common `required` fields kept.
//   - A `pattern`, or a `patternProperties` key, that uses a regular expression construct the
//     endpoint cannot compile is removed: a Unicode property escape (\p{...}, \P{...}), the
//     octal NUL escape (\0), or an unescaped lookahead ((?=...), (?!...)) outside a character
//     class.
//   - A numeric bound (`minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`) that is an
//     integer outside the 64-bit range is removed.
//   - A property whose `oneOf` or `anyOf` is only distinct `const` branches becomes an `enum`.
//   - An `array` schema without `items` (and without `$ref`) receives `items: {}` (an empty
//     schema, which accepts any element).
//   - When the tool has `strict: true`, every optional property in any object schema is added to
//     `required` and its schema is made nullable (null is added to every keyword that limits the
//     value, or the schema is wrapped in an `anyOf` with a null branch), and every object schema
//     with `properties` gets `additionalProperties: false`, as OpenAI's strict mode requires. A
//     schema that cannot be closed without changing what the tool accepts cannot be made strict:
//     the tool is sent with `strict: false` and none of the strict rewrites. Such a schema either
//     accepts more than its properties (`additionalProperties` true or a schema, or an object type
//     without `properties`), has an unconstrained array item schema (`items: {}`, including one
//     filled from a missing `items`), or combines its objects with other schemas (an `allOf` of
//     more than one branch, `if`, `then`, `else`, `not`, `dependentSchemas`, `dependencies`, or
//     `properties` beside `$ref`, `allOf`, `anyOf` or `oneOf`), where closing each object would
//     refuse input the combination accepts. A single-branch `allOf` combines nothing and stays
//     closable.

// unionToEnumBranches is how many branches a union needs before it becomes an enum. Smaller
// unions are left as written.
const unionToEnumBranches = 8

// boundKeywords are the numeric bound keywords of JSON Schema.
var boundKeywords = [...]string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"}

// subschemaMaps are keywords whose value is an object of subschemas.
var subschemaMaps = [...]string{"properties", "$defs", "definitions", "dependentSchemas", "dependencies"}

// combinedKeywords are the keywords that test a schema's object against other schemas or against
// the negation of one. An object under one of them cannot be closed (see schemaIsOpen).
var combinedKeywords = [...]string{"if", "then", "else", "not", "dependentSchemas", "dependencies"}

// subschemas are keywords whose value is a subschema or a list of them.
var subschemas = [...]string{
	"items", "prefixItems", "contains", "additionalProperties", "propertyNames",
	"unevaluatedProperties", "unevaluatedItems", "additionalItems", "contentSchema",
	"anyOf", "oneOf", "allOf", "not", "if", "then", "else",
}

// fixToolSchemas applies the rules to the parameters of every function tool in tools, also
// those inside namespace tools (the Codex app groups MCP tools that way), and reports whether
// it changed any. A tool it changes is replaced in the slice; the others stay as sent. A strict
// tool whose schema cannot be made strict is sent with strict false; lose, when not nil, is told
// the tool's path (path, then its index in tools) and why.
func fixToolSchemas(tools []json.RawMessage, path string, lose func(path, detail string)) bool {
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
			var strict bool
			_ = json.Unmarshal(o["strict"], &strict)
			if p, ok, strictOff := fixSchemaForMode(o["parameters"], strict); ok {
				o["parameters"] = p
				if strictOff {
					o["strict"] = raw(false)
					if lose != nil {
						lose(path+"["+strconv.Itoa(i)+"]", "strict mode was turned off: the schema cannot be closed without changing what it accepts, which strict mode cannot express")
					}
				}
				tools[i] = raw(o)
				changed = true
			}
		case "namespace":
			var nested []json.RawMessage
			if json.Unmarshal(o["tools"], &nested) == nil && fixToolSchemas(nested, path+"["+strconv.Itoa(i)+"].tools", lose) {
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
	out, changed, _ := fixSchemaForMode(schema, false)
	return out, changed
}

// fixSchemaForMode is fixSchema for a tool that is strict or not. strictOff is true when the tool
// is strict but its schema cannot be made strict: the result then has only the rewrites that
// apply to any tool, and the caller must send the tool with strict false.
func fixSchemaForMode(schema json.RawMessage, strict bool) (out json.RawMessage, changed, strictOff bool) {
	dec := json.NewDecoder(bytes.NewReader(schema))
	dec.UseNumber()
	var root any
	if dec.Decode(&root) != nil {
		return schema, false, false
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return schema, false, false
	}
	changed = flattenRootUnion(obj)
	if dropRejected(obj) {
		changed = true
	}
	if props, ok := obj["properties"].(map[string]any); ok {
		for _, p := range props {
			if prop, ok := p.(map[string]any); ok && unionToEnum(prop) {
				changed = true
			}
		}
	}
	if addMissingArrayItems(obj) {
		changed = true
	}
	if strict {
		if schemaIsOpen(obj) {
			strictOff, changed = true, true
		} else {
			if closeObjects(obj) {
				changed = true
			}
			if makeOptionalPropsNullable(obj) {
				changed = true
			}
		}
	}
	if !changed {
		return schema, false, false
	}
	return raw(obj), true, strictOff
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

// rejectedPattern reports whether a regular expression uses \p{...}, \P{...}, \0, or an unescaped
// lookahead ((?=...), (?!...)) outside a character class, which the endpoint fails to compile.
// All of these are legal in the dialects clients write; the endpoint's validator does not take them.
func rejectedPattern(p string) bool {
	inClass := false
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' {
			if i+1 >= len(p) {
				break
			}
			switch p[i+1] {
			case '0':
				return true
			case 'p', 'P':
				if i+2 < len(p) && p[i+2] == '{' {
					return true
				}
			}
			i++ // the escaped character is not the start of another escape, as in \\p or \(?!
			continue
		}
		if inClass {
			if p[i] == ']' {
				inClass = false
			}
			continue
		}
		if p[i] == '[' {
			inClass = true
			if i+1 < len(p) && p[i+1] == '^' {
				i++
			}
			if i+1 < len(p) && p[i+1] == ']' {
				i++
			}
			continue
		}
		if p[i] == '(' && i+2 < len(p) && p[i+1] == '?' && (p[i+2] == '=' || p[i+2] == '!') {
			return true
		}
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

// flattenRootUnion rewrites a root anyOf/oneOf schema into an object schema. Existing root
// properties win; properties introduced only by branches keep a single copy when identical or an
// anyOf of distinct branch schemas; fields required in every object branch stay required.
func flattenRootUnion(obj map[string]any) bool {
	_, hasAny := obj["anyOf"]
	_, hasOne := obj["oneOf"]
	if !hasAny && !hasOne {
		return false
	}

	defs := localDefs(obj)
	var branches []map[string]any
	for _, key := range []string{"anyOf", "oneOf"} {
		if rawList, ok := obj[key].([]any); ok {
			collectRootObjectBranches(rawList, defs, map[string]bool{}, 0, &branches)
		}
		delete(obj, key)
	}

	props, ok := obj["properties"].(map[string]any)
	if !ok || props == nil {
		props = map[string]any{}
	}
	rootProps := make(map[string]bool, len(props))
	for k := range props {
		rootProps[k] = true
	}

	variants := map[string][]any{}
	var commonReq map[string]bool
	for i, br := range branches {
		if bp, ok := br["properties"].(map[string]any); ok {
			for _, name := range slices.Sorted(maps.Keys(bp)) {
				if rootProps[name] {
					continue
				}
				val := bp[name]
				cur, seen := variants[name]
				if !seen {
					variants[name] = []any{val}
					continue
				}
				dup := false
				for _, prev := range cur {
					if reflect.DeepEqual(prev, val) {
						dup = true
						break
					}
				}
				if !dup {
					variants[name] = append(cur, val)
				}
			}
		}
		brReq := stringSet(br["required"])
		if i == 0 {
			commonReq = brReq
		} else {
			for k := range commonReq {
				if !brReq[k] {
					delete(commonReq, k)
				}
			}
		}
	}

	for _, name := range slices.Sorted(maps.Keys(variants)) {
		list := variants[name]
		if len(list) == 1 {
			props[name] = list[0]
		} else {
			props[name] = map[string]any{"anyOf": list}
		}
	}

	obj["type"] = "object"
	obj["properties"] = props

	if len(commonReq) > 0 {
		reqList, reqSet := stringSliceAndSet(obj["required"])
		for _, name := range slices.Sorted(maps.Keys(commonReq)) {
			if !reqSet[name] {
				reqList = append(reqList, name)
				reqSet[name] = true
			}
		}
		if len(reqList) > 0 {
			obj["required"] = reqList
		}
	}
	return true
}

func localDefs(obj map[string]any) map[string]map[string]any {
	defs := map[string]map[string]any{}
	for _, key := range []string{"$defs", "definitions"} {
		if m, ok := obj[key].(map[string]any); ok {
			for name, v := range m {
				if sm, ok := v.(map[string]any); ok {
					defs["#/"+key+"/"+name] = sm
				}
			}
		}
	}
	return defs
}

func collectRootObjectBranches(list []any, defs map[string]map[string]any, seenRefs map[string]bool, depth int, out *[]map[string]any) {
	if depth > 16 {
		return
	}
	for _, item := range list {
		bm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if ref, ok := bm["$ref"].(string); ok {
			if seenRefs[ref] {
				continue
			}
			target, found := defs[ref]
			if !found {
				continue
			}
			seenRefs[ref] = true
			bm = target
		}
		hasNested := false
		for _, uk := range []string{"anyOf", "oneOf"} {
			if sub, ok := bm[uk].([]any); ok {
				hasNested = true
				collectRootObjectBranches(sub, defs, seenRefs, depth+1, out)
			}
		}
		if hasNested && bm["properties"] == nil {
			continue
		}
		if schemaCanBeObject(bm) {
			*out = append(*out, bm)
		}
	}
}

func schemaCanBeObject(s map[string]any) bool {
	t, ok := s["type"]
	if !ok {
		return true
	}
	switch v := t.(type) {
	case string:
		return strings.EqualFold(v, "object")
	case []any:
		for _, item := range v {
			if str, ok := item.(string); ok && strings.EqualFold(str, "object") {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// addMissingArrayItems adds items: {} to every array schema (without $ref or items)
// under v, recursing only into schema-valued keywords.
func addMissingArrayItems(v any) bool {
	changed := false
	switch s := v.(type) {
	case []any:
		for _, item := range s {
			if addMissingArrayItems(item) {
				changed = true
			}
		}
	case map[string]any:
		if hasArrayType(s["type"]) {
			if _, hasRef := s["$ref"]; !hasRef {
				if items, hasItems := s["items"]; !hasItems || items == nil {
					s["items"] = map[string]any{}
					changed = true
				}
			}
		}
		if pp, ok := s["patternProperties"].(map[string]any); ok {
			for _, sub := range pp {
				if addMissingArrayItems(sub) {
					changed = true
				}
			}
		}
		for _, k := range subschemaMaps {
			if m, ok := s[k].(map[string]any); ok {
				for _, sub := range m {
					if addMissingArrayItems(sub) {
						changed = true
					}
				}
			}
		}
		for _, k := range subschemas {
			switch sub := s[k].(type) {
			case map[string]any, []any:
				if addMissingArrayItems(sub) {
					changed = true
				}
			}
		}
	}
	return changed
}

func hasArrayType(t any) bool {
	switch v := t.(type) {
	case string:
		return strings.EqualFold(v, "array")
	case []any:
		for _, item := range v {
			if str, ok := item.(string); ok && strings.EqualFold(str, "array") {
				return true
			}
		}
	}
	return false
}

// eachSchema calls fn on every schema under v (v included) and reports whether any call did. Only
// keywords that hold schemas are entered, so an `enum`, `const` or `default` that happens to look
// like a schema is not visited. fn runs on a schema before the schemas under it.
func eachSchema(v any, fn func(map[string]any) bool) bool {
	changed := false
	switch s := v.(type) {
	case []any:
		for _, item := range s {
			if eachSchema(item, fn) {
				changed = true
			}
		}
	case map[string]any:
		if fn(s) {
			changed = true
		}
		if pp, ok := s["patternProperties"].(map[string]any); ok {
			for _, sub := range pp {
				if eachSchema(sub, fn) {
					changed = true
				}
			}
		}
		for _, k := range subschemaMaps {
			if m, ok := s[k].(map[string]any); ok {
				for _, sub := range m {
					if eachSchema(sub, fn) {
						changed = true
					}
				}
			}
		}
		for _, k := range subschemas {
			switch sub := s[k].(type) {
			case map[string]any, []any:
				if eachSchema(sub, fn) {
					changed = true
				}
			}
		}
	}
	return changed
}

// schemaIsOpen reports whether a schema under v cannot be closed without changing what it accepts,
// which strict mode cannot express. That is a schema that accepts keys beyond its properties:
// `additionalProperties` true or a schema, or an object type with no `properties` (a free-form
// map), or an unconstrained array item schema (`items: {}`). It is also a schema that combines
// its object with other schemas, where closing the object would refuse input the combination
// accepts: an `allOf` of more than one branch, any of the `combinedKeywords`, or `properties`
// beside `$ref`, `allOf`, `anyOf` or `oneOf`.
func schemaIsOpen(v any) bool {
	open := false
	eachSchema(v, func(s map[string]any) bool {
		if ap, ok := s["additionalProperties"]; ok && ap != false {
			open = true
		}
		if _, ok := s["properties"]; !ok && hasObjectType(s["type"]) {
			open = true
		}
		if items, ok := s["items"].(map[string]any); ok && len(items) == 0 {
			open = true
		}
		if branches, ok := s["allOf"].([]any); ok && len(branches) > 1 {
			open = true
		}
		for _, k := range combinedKeywords {
			if _, ok := s[k]; ok {
				open = true
			}
		}
		if _, ok := s["properties"]; ok {
			for _, k := range [...]string{"$ref", "allOf", "anyOf", "oneOf"} {
				if _, ok := s[k]; ok {
					open = true
				}
			}
		}
		return false
	})
	return open
}

func hasObjectType(t any) bool {
	if s, ok := t.(string); ok {
		return s == "object"
	}
	if l, ok := t.([]any); ok {
		for _, x := range l {
			if x == "object" {
				return true
			}
		}
	}
	return false
}

// closeObjects gives every schema with a `properties` key (an empty one counts) and no
// `additionalProperties` the value false, and reports whether it changed any.
func closeObjects(v any) bool {
	return eachSchema(v, func(s map[string]any) bool {
		if _, ok := s["properties"].(map[string]any); !ok {
			return false
		}
		if _, ok := s["additionalProperties"]; ok {
			return false
		}
		s["additionalProperties"] = false
		return true
	})
}

// makeOptionalPropsNullable enforces Codex strict mode's requirement that every property of every
// object schema is listed in required: properties missing from required are appended to it and
// made nullable.
func makeOptionalPropsNullable(v any) bool {
	return eachSchema(v, func(s map[string]any) bool {
		props, ok := s["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			return false
		}
		reqList, reqSet := stringSliceAndSet(s["required"])
		added := false
		for _, name := range slices.Sorted(maps.Keys(props)) {
			if reqSet[name] {
				continue
			}
			reqList = append(reqList, name)
			reqSet[name] = true
			added = true
			if pm, ok := props[name].(map[string]any); ok {
				props[name] = nullableSchema(pm)
			}
		}
		if added {
			s["required"] = reqList
		}
		return added
	})
}

// nullableSchema makes a schema accept null. A schema whose only limit is a plain `type`, an
// `enum`, an `anyOf` or a `oneOf` gets null in it. Any other schema (a `$ref`, `const` or
// `allOf`, or several of those four keywords together) is wrapped in `anyOf` with a null branch,
// since null added to one keyword would still fail the other: `description` and `title` move
// outside.
func nullableSchema(pm map[string]any) map[string]any {
	if schemaAllowsNull(pm) {
		return pm
	}
	_, hasRef := pm["$ref"]
	_, hasConst := pm["const"]
	_, hasAllOf := pm["allOf"]
	types, typeList := pm["type"].([]any)
	t, typeString := pm["type"].(string)
	typeString = typeString && t != ""
	hasType := typeList || typeString
	vals, hasEnum := pm["enum"].([]any)
	anyBranches, hasAnyOf := pm["anyOf"].([]any)
	oneBranches, hasOneOf := pm["oneOf"].([]any)
	limits := 0
	for _, has := range []bool{hasType, hasEnum, hasAnyOf, hasOneOf} {
		if has {
			limits++
		}
	}
	switch {
	case hasRef || hasConst || hasAllOf || limits > 1:
		outer := map[string]any{}
		inner := maps.Clone(pm)
		for _, meta := range []string{"description", "title"} {
			if v, ok := inner[meta]; ok {
				outer[meta] = v
				delete(inner, meta)
			}
		}
		outer["anyOf"] = []any{inner, map[string]any{"type": "null"}}
		return outer
	case hasAnyOf:
		pm["anyOf"] = append(anyBranches, map[string]any{"type": "null"})
	case hasOneOf:
		pm["oneOf"] = append(oneBranches, map[string]any{"type": "null"})
	case typeList:
		pm["type"] = append(types, "null")
	case typeString:
		pm["type"] = []any{t, "null"}
	case hasEnum:
		pm["enum"] = append(vals, nil)
	}
	return pm
}

// schemaAllowsNull reports whether null passes the schema: it has at least one of `type`, `enum`,
// `const`, `anyOf` and `oneOf`, and null passes every one it has. A `$ref` or `allOf` is not
// followed, so a schema with one is taken to refuse null.
func schemaAllowsNull(pm map[string]any) bool {
	if _, ok := pm["$ref"]; ok {
		return false
	}
	if _, ok := pm["allOf"]; ok {
		return false
	}
	limited := false
	if t, ok := pm["type"]; ok {
		limited = true
		allowed := false
		switch t := t.(type) {
		case string:
			allowed = strings.EqualFold(t, "null")
		case []any:
			for _, item := range t {
				if str, ok := item.(string); ok && strings.EqualFold(str, "null") {
					allowed = true
				}
			}
		}
		if !allowed {
			return false
		}
	}
	if c, ok := pm["const"]; ok {
		limited = true
		if c != nil {
			return false
		}
	}
	if vals, ok := pm["enum"].([]any); ok {
		limited = true
		if !slices.Contains(vals, nil) {
			return false
		}
	}
	for _, uk := range []string{"anyOf", "oneOf"} {
		if branches, ok := pm[uk].([]any); ok {
			limited = true
			allowed := false
			for _, b := range branches {
				if bm, ok := b.(map[string]any); ok && schemaAllowsNull(bm) {
					allowed = true
				}
			}
			if !allowed {
				return false
			}
		}
	}
	return limited
}

func stringSet(v any) map[string]bool {
	_, set := stringSliceAndSet(v)
	return set
}

func stringSliceAndSet(v any) ([]any, map[string]bool) {
	set := map[string]bool{}
	var out []any
	arr, ok := v.([]any)
	if !ok {
		return out, set
	}
	for _, item := range arr {
		if s, ok := item.(string); ok {
			if !set[s] {
				set[s] = true
				out = append(out, s)
			}
		}
	}
	return out, set
}
