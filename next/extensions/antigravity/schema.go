package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
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
//   - if, then and else, which it lacks too, are removed after the properties of each then and else
//     move into the schema that holds them (a property the schema has stays as it is), so a model
//     sees them as plain optional properties, as in today's core; a branch's required names do not
//     move;
//   - allOf, which Cloud Code's schema lacks, merges into the schema that holds it: the branches'
//     required names join its required, and their other keys fill the keys it lacks, recursively
//     (its own values win). As in today's core this comes after the hints and the conditionals,
//     deepest first, and before unions and type lists are flattened;
//   - a type list such as ["string","null"] becomes the one type with nullable true;
//   - a union with one non-null branch becomes that branch, nullable; for Claude models, whose
//     backend rejects unions in function declarations, every union becomes its strongest branch
//     (object over array over scalar) with the other types hinted ("Accepts: a | b");
//   - other JSON Schema keywords are removed, and so is title, except in a response schema (Google's
//     Schema type has a title field, and today's core keeps it there) and, for Claude and Gemini 3 Pro,
//     in a function's parameters and response;
//   - for Claude and Gemini 3 Pro (the models today's core marks this way), a function's
//     parameters get a required placeholder in every object that requires nothing: "reason" (a
//     string) in one with no properties, "_" (a boolean) in one with properties and none
//     required. Claude's VALIDATED mode, which the extension asks for, wants a required property
//     in every object. cleanTools reports where each placeholder went, and placeholders.strip
//     removes it from the model's function calls, in every client format.

// schemaMode says which rules apply to a schema.
type schemaMode struct {
	response     bool // a response schema, not a function's parameters
	claude       bool // a function declaration for a Claude model
	keepTitle    bool // keep title: Google's Schema has the field; a function's parameters and response lose it for other models
	placeholders bool // add the placeholders to a function's parameters
}

// placeholderReason describes the "reason" placeholder, as today's core words it.
const placeholderReason = "Brief explanation of why you are calling this tool"

// placeholderStep is one step from a function call's arguments to an object: into the property
// named key, or, when any is set, into every element of an array or every value of an object.
type placeholderStep struct {
	key string
	any bool
}

// placeholderSite is an object that got a placeholder: the steps to it from the arguments, and
// the placeholder's name.
type placeholderSite struct {
	path []placeholderStep
	name string
}

// placeholders lists, by function name, where cleanTools added a placeholder to the parameters.
type placeholders map[string][]placeholderSite

// hintedConstraints move into the description, in this order, as today's core moves them.
var hintedConstraints = []string{
	"minLength", "maxLength", "exclusiveMinimum", "exclusiveMaximum", "pattern", "minItems", "maxItems",
	"uniqueItems", "contains", "format", "default", "examples", "minimum", "maximum", "multipleOf",
}

// removedKeywords are removed without a hint.
var removedKeywords = []string{
	"$schema", "$defs", "definitions", "$id", "id", "$anchor", "$vocabulary", "$dynamicRef", "$dynamicAnchor", "$comment",
	"propertyNames", "patternProperties", "dependentRequired", "dependentSchemas", "dependencies",
	"not", "unevaluatedProperties", "unevaluatedItems", "additionalItems", "contentSchema",
	"contentMediaType", "contentEncoding", "readOnly", "writeOnly", "deprecated", "discriminator",
	"enumDescriptions", "enumTitles", "prefill", "encrypted",
}

// maxRefDepth bounds $ref inlining, so a schema that refers to itself ends.
const maxRefDepth = 16

// cleanTools returns a request's tools with every function declaration's schemas cleaned: its
// parameters (parametersJsonSchema renamed to parameters) and its response. group is true for
// Claude and Gemini 3 Pro, which keep titles (also in a function's response) and get placeholders
// in their parameters; the second result says where the placeholders went, leaving out any that
// a client schema at the same path
// (another union branch, whole as the client wrote it even where Claude's cleaning keeps one; a
// tuple item; a function declared again under the same name) declares a name the placeholder
// has, as a property or in required: stripping would take the client's key, so that placeholder
// is left in the model's calls, as in today's core. Every schema with properties and no type gets
// type "object".
func cleanTools(raw json.RawMessage, claude, group bool) (json.RawMessage, placeholders) {
	var tools []map[string]json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return raw, nil
	}
	var added placeholders
	declared := map[string]declaredNames{} // by function name
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
					cleaned, sites, names := cleanParameters(p, schemaMode{claude: claude, keepTitle: group, placeholders: group})
					d["parameters"] = cleaned
					var name string
					_ = json.Unmarshal(d["name"], &name)
					if declared[name] == nil {
						declared[name] = declaredNames{}
					}
					declared[name].merge(names)
					if len(sites) > 0 {
						if added == nil {
							added = placeholders{}
						}
						added[name] = append(added[name], sites...)
					}
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
					d["response"] = cleanSchema(r, schemaMode{response: true, keepTitle: group})
				}
			}
			t[key], _ = json.Marshal(decls)
		}
	}
	out, err := json.Marshal(tools)
	if err != nil {
		return raw, nil
	}
	for name, sites := range added {
		kept := slices.DeleteFunc(sites, func(s placeholderSite) bool { return declared[name][pathKey(s.path)][s.name] })
		if len(kept) == 0 {
			delete(added, name)
		} else {
			added[name] = kept
		}
	}
	return out, added
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
		gen["responseSchema"] = cleanSchema(v, schemaMode{response: true, keepTitle: true})
		changed = true
	}
	return changed
}

// cleanSchema returns a schema cleaned under mode.
func cleanSchema(raw json.RawMessage, mode schemaMode) json.RawMessage {
	out, _, _ := cleanParameters(raw, mode)
	return out
}

// cleanParameters returns a schema cleaned under mode, where it added placeholders (only when
// mode asks for them), and the property names the client's schemas declare.
func cleanParameters(raw json.RawMessage, mode schemaMode) (json.RawMessage, []placeholderSite, declaredNames) {
	var root any
	if json.Unmarshal(raw, &root) != nil {
		return raw, nil, nil
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
	cleaned := cleanTree(root, defs, mode)
	typeObjects(cleaned)
	var sites []placeholderSite
	declared := declaredNames{}
	if mode.placeholders && !mode.response {
		// The names client schemas declare are read from a tree whose unions are whole: for Claude,
		// cleaned has cut each union to one branch, and the others' names are the client's too.
		whole := cleaned
		if mode.claude {
			whole = cleanTree(root, defs, schemaMode{keepTitle: mode.keepTitle})
		}
		collectDeclared(whole, nil, declared)
		addPlaceholders(cleaned, nil, &sites)
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return raw, nil, nil
	}
	return out, sites, declared
}

// typeObjects gives type "object" to every schema that has properties and no type, as today's
// core does.
func typeObjects(node any) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	schemaChildren(m, nil, func(child any, _ []placeholderStep) { typeObjects(child) })
	if _, typed := m["type"]; typed {
		return
	}
	if _, ok := m["properties"].(map[string]any); ok {
		m["type"] = "object"
	}
}

// pathKey names the values a path leads to, to compare the schemas at one path.
func pathKey(path []placeholderStep) string {
	parts := make([]string, len(path))
	for i, st := range path {
		if st.any {
			parts[i] = "*"
		} else {
			parts[i] = strconv.Quote(st.key)
		}
	}
	return strings.Join(parts, "/")
}

// declaredNames lists the property names client schemas declare, by pathKey of the values they
// describe.
type declaredNames map[string]map[string]bool

func (d declaredNames) merge(other declaredNames) {
	for k, names := range other {
		if d[k] == nil {
			d[k] = map[string]bool{}
		}
		for n := range names {
			d[k][n] = true
		}
	}
}

// schemaChildren calls fn for each subschema of a cleaned schema m (allOf is merged by then) with
// the path to the values it describes: a property's, an array element's, or for a union branch
// m's own.
func schemaChildren(m map[string]any, path []placeholderStep, fn func(child any, path []placeholderStep)) {
	step := func(s placeholderStep) []placeholderStep { return append(slices.Clone(path), s) }
	if props, ok := m["properties"].(map[string]any); ok {
		for _, name := range slices.Sorted(maps.Keys(props)) {
			fn(props[name], step(placeholderStep{key: name}))
		}
	}
	for _, k := range []string{"items", "prefixItems"} {
		switch items := m[k].(type) {
		case map[string]any:
			fn(items, step(placeholderStep{any: true}))
		case []any:
			for _, it := range items {
				fn(it, step(placeholderStep{any: true}))
			}
		}
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		if branches, ok := m[k].([]any); ok {
			for _, b := range branches {
				fn(b, path)
			}
		}
	}
}

// collectDeclared records the names every schema under node declares, as properties or in required
// (a required name is the client's too), before any placeholder is added.
func collectDeclared(node any, path []placeholderStep, declared declaredNames) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	key := pathKey(path)
	note := func(name string) {
		if declared[key] == nil {
			declared[key] = map[string]bool{}
		}
		declared[key][name] = true
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for name := range props {
			note(name)
		}
	}
	if list, ok := m["required"].([]any); ok {
		for _, r := range list {
			if name, ok := r.(string); ok {
				note(name)
			}
		}
	}
	schemaChildren(m, path, func(child any, p []placeholderStep) { collectDeclared(child, p, declared) })
}

// addPlaceholders adds a placeholder to every object schema under node, as today's core does, and
// records each in sites. path leads to the values node describes. An object is a schema of type
// "object" (typeObjects has typed every schema with properties); one that is a union (it has anyOf
// or oneOf) gets none.
//
// shortcut: an object with no declared properties gets "reason", which strip removes from the
// model's calls wherever it stands in that object, so a key of that name the model means as data
// (a free-form map's) is removed too; today's core leaves such a key where the object takes extra
// keys. Do the same when a client's free-form map is seen to lose a "reason".
func addPlaceholders(node any, path []placeholderStep, sites *[]placeholderSite) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	schemaChildren(m, path, func(child any, p []placeholderStep) { addPlaceholders(child, p, sites) })
	if _, ok := m["anyOf"].([]any); ok {
		return
	}
	if _, ok := m["oneOf"].([]any); ok {
		return
	}
	own, hasOwn := m["properties"]
	props, isMap := own.(map[string]any)
	if hasOwn && !isMap {
		return // properties: null, or not an object, is left as it is
	}
	if m["type"] != "object" {
		return
	}
	if props == nil {
		props = map[string]any{}
	}
	switch {
	case len(props) == 0:
		props["reason"] = map[string]any{"type": "string", "description": placeholderReason}
		m["required"] = []any{"reason"}
		*sites = append(*sites, placeholderSite{path: path, name: "reason"})
	default:
		if list, ok := m["required"].([]any); ok {
			for _, r := range list {
				if name, ok := r.(string); ok {
					if _, declared := props[name]; declared {
						return
					}
				}
			}
		}
		if _, has := props["_"]; !has {
			props["_"] = map[string]any{"type": "boolean"}
			*sites = append(*sites, placeholderSite{path: path, name: "_"})
		}
		m["required"] = []any{"_"}
	}
	if !hasOwn {
		m["properties"] = props
	}
}

// strip removes the placeholders from the function calls of one GenerateContent response (or
// stream chunk). Any other chunk, and a call with no placeholder in it, comes back as it is.
func (p placeholders) strip(data []byte) []byte {
	if len(p) == 0 || !bytes.Contains(data, []byte(`"functionCall"`)) {
		return data
	}
	var chunk map[string]json.RawMessage
	if json.Unmarshal(data, &chunk) != nil {
		return data
	}
	var cands []map[string]json.RawMessage
	if json.Unmarshal(chunk["candidates"], &cands) != nil {
		return data
	}
	changed := false
	for _, cand := range cands {
		var content map[string]json.RawMessage
		var parts []map[string]json.RawMessage
		if json.Unmarshal(cand["content"], &content) != nil || json.Unmarshal(content["parts"], &parts) != nil {
			continue
		}
		partsChanged := false
		for _, part := range parts {
			var call map[string]json.RawMessage
			if json.Unmarshal(part["functionCall"], &call) != nil {
				continue
			}
			var name string
			_ = json.Unmarshal(call["name"], &name)
			sites := p[name]
			if len(sites) == 0 || len(call["args"]) == 0 {
				continue
			}
			if args, ok := stripSites(call["args"], sites); ok {
				call["args"] = args
				part["functionCall"], _ = marshalPlain(call)
				partsChanged = true
			}
		}
		if partsChanged {
			content["parts"], _ = marshalPlain(parts)
			cand["content"], _ = marshalPlain(content)
			changed = true
		}
	}
	if !changed {
		return data
	}
	chunk["candidates"], _ = marshalPlain(cands)
	out, err := marshalPlain(chunk)
	if err != nil {
		return data
	}
	return out
}

// stripSites removes the placeholder of each site from a call's arguments, and reports whether it
// removed any.
func stripSites(args json.RawMessage, sites []placeholderSite) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber() // numbers come back as they were written
	var root any
	if dec.Decode(&root) != nil {
		return args, false
	}
	removed := false
	for _, s := range sites {
		targets := []any{root}
		for _, st := range s.path {
			var next []any
			for _, t := range targets {
				switch v := t.(type) {
				case map[string]any:
					if st.any {
						for _, c := range v {
							next = append(next, c)
						}
					} else if c, ok := v[st.key]; ok {
						next = append(next, c)
					}
				case []any:
					if st.any {
						next = append(next, v...)
					}
				}
			}
			targets = next
		}
		for _, t := range targets {
			if m, ok := t.(map[string]any); ok {
				if _, has := m[s.name]; has {
					delete(m, s.name)
					removed = true
				}
			}
		}
	}
	if !removed {
		return args, false
	}
	out, err := marshalPlain(root)
	if err != nil {
		return args, false
	}
	return out, true
}

// marshalPlain is json.Marshal without the escaping of <, > and &, which would change text the
// model wrote in a call's arguments.
func marshalPlain(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// cleanTree cleans a whole schema in today's core's order: keywords and hints (clean), then the
// properties of if/then/else merged (mergeConditionals), then allOf merged (mergeAllOf), then
// unions and type lists flattened (flatten).
func cleanTree(root any, defs map[string]any, mode schemaMode) any {
	return flatten(mergeAllOf(mergeConditionals(clean(root, defs, mode, 0))), mode)
}

// walkSchema calls fn on every schema under v, children first, as clean reaches them: a
// properties map holds schemas by name and is not one itself. fn returns what replaces the schema.
func walkSchema(v any, fn func(map[string]any) any) any {
	switch x := v.(type) {
	case []any:
		for i, val := range x {
			x[i] = walkSchema(val, fn)
		}
		return x
	case map[string]any:
		for k, val := range x {
			if k == "properties" {
				if props, ok := val.(map[string]any); ok {
					for name, p := range props {
						props[name] = walkSchema(p, fn)
					}
				}
				continue
			}
			x[k] = walkSchema(val, fn)
		}
		return fn(x)
	}
	return v
}

// mergeConditionals removes every if, then and else, deepest first, after merging the properties
// of each then and else into the schema that holds it, as today's core does (mergeConditionals in
// core/internal/util/gemini_schema.go): then first, and a property the schema already has stays as
// it is. Cloud Code's schema has no conditionals, but a property only a branch declares is one the
// client accepts, and the model sees it here as a plain optional one. Nothing but properties
// moves: a branch's required names and its other keys are dropped with the keywords.
func mergeConditionals(v any) any {
	return walkSchema(v, func(m map[string]any) any {
		for _, k := range []string{"then", "else"} {
			branch, _ := m[k].(map[string]any)
			props, _ := branch["properties"].(map[string]any)
			if len(props) == 0 {
				continue
			}
			dest, ok := m["properties"].(map[string]any)
			if !ok {
				if _, has := m["properties"]; has {
					continue // not a properties map: nothing to merge into
				}
				dest = map[string]any{}
			}
			for name, p := range props {
				if _, has := dest[name]; !has {
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

// mergeAllOf merges every allOf into the schema that holds it, deepest first, as today's core
// does (mergeAllOf in core/internal/util/gemini_schema.go): a branch's required names are added to
// the schema's, and its other keys fill the keys the schema lacks, recursively, the schema's own
// values winning. A branch that is not an object adds nothing, and an allOf that is not a list is
// removed (today's core keeps it as a property named allOf).
func mergeAllOf(v any) any {
	return walkSchema(v, func(m map[string]any) any {
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
					// Merged into the branch already; a conditional (if, then, else) is gone
					// after mergeConditionals.
				default:
					fillMissing(m, k, bm[k])
				}
			}
		}
		return m
	})
}

// fillMissing sets m[k] to v when m has no k, and when both are objects fills each of v's keys the
// same way.
func fillMissing(m map[string]any, k string, v any) {
	cur, has := m[k]
	if !has {
		m[k] = v
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

// flatten flattens every union and type list under v, children first: a union as flattenUnion
// says, and a type list such as ["string","null"] to its first type other than null, nullable
// when it has null, with the other types hinted.
func flatten(v any, mode schemaMode) any {
	return walkSchema(v, func(out map[string]any) any {
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
	})
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
