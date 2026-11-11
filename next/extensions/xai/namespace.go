package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// Namespace tools are how the Codex app groups tools: {"type":"namespace","name":N,"tools":[...]}.
// xAI has no such tool, so, as today's core does (internal/runtime/executor/xai_executor_request.go
// and xai_executor_response.go), the extension sends a namespace's tools as functions and restores
// the namespace in the reply:
//
//   - flattened: each child function is sent as N__child (no prefix is added to a name that has
//     one or starts with mcp__), and xAI's call to it comes back as the call to child in namespace N;
//   - folded, when the flattened tools would be more than xAI takes (maxTools): the namespace is
//     one function named N whose arguments are {"name": child, "arguments": {...}}, its description
//     lists the children and their schemas, and xAI's call to it comes back as the call to child
//     in namespace N. Folded namespaces come first when the tools are cut to maxTools.
//
// A function call in the history that names a namespace, and a tool_choice that does, are written
// the way the tools were sent. tool_search tools in a namespace are dropped. The first tool of a
// name stays when a name repeats.

const namespaceType = "namespace"

// nsRef says what a name sent to xAI stands for.
type nsRef struct {
	namespace, name string
	dispatcher      bool
	children        map[string]struct{}
}

// namespaceTools is how one request's namespace tools were sent.
type namespaceTools struct {
	fold  bool
	refs  map[string]*nsRef
	names map[string]bool // names of the functions sent
}

// qualifyName is the name a namespace's child is sent under.
func qualifyName(namespace, name string) string {
	namespace, name = strings.TrimSpace(namespace), strings.TrimSpace(name)
	if namespace == "" || name == "" || strings.HasPrefix(name, "mcp__") {
		return name
	}
	prefix := namespace
	if !strings.HasSuffix(prefix, "__") {
		prefix += "__"
	}
	if strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

func isTool(m map[string]any) bool { return m["type"] == "function" || m["type"] == "custom" }

// flattenedCount is how many tools the request has once namespaces are flattened.
func flattenedCount(tools []any) int {
	seen := map[string]bool{}
	count := 0
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			count++
			continue
		}
		switch {
		case m["type"] == namespaceType:
			children, ok := m["tools"].([]any)
			if !ok {
				count++
				continue
			}
			for _, c := range children {
				cm, _ := c.(map[string]any)
				if cm == nil || cm["type"] == "tool_search" {
					continue
				}
				if q := qualifyName(str(m, "name"), str(cm, "name")); q != "" {
					if seen[q] {
						continue
					}
					seen[q] = true
				}
				count++
			}
		case isTool(m):
			if name := strings.TrimSpace(str(m, "name")); name != "" {
				if seen[name] {
					continue
				}
				seen[name] = true
			}
			count++
		case m["type"] != "tool_search":
			count++
		}
	}
	return count
}

// rewriteNamespaces sends the namespace tools in tools as functions. It returns nil when there
// are none.
func rewriteNamespaces(tools []any) ([]any, *namespaceTools) {
	has := false
	for _, t := range tools {
		if m, ok := t.(map[string]any); ok && m["type"] == namespaceType {
			has = true
		}
	}
	if !has {
		return tools, nil
	}
	st := &namespaceTools{fold: flattenedCount(tools) > maxTools, refs: map[string]*nsRef{}, names: map[string]bool{}}
	claimed := map[string]bool{}
	type entry struct {
		tool       any
		dispatcher bool
	}
	var out []entry
	seen := map[string]bool{}
	add := func(t any, name string, dispatcher bool) bool {
		if name != "" {
			if seen[name] {
				return false
			}
			seen[name] = true
		}
		out = append(out, entry{t, dispatcher})
		return true
	}
	for _, t := range tools {
		m, ok := t.(map[string]any)
		if !ok {
			out = append(out, entry{tool: t})
			continue
		}
		if m["type"] != namespaceType {
			name := ""
			if isTool(m) {
				name = strings.TrimSpace(str(m, "name"))
				claimed[name] = name != ""
			}
			if add(t, name, false) && name != "" {
				st.names[name] = true
			}
			continue
		}
		ns := strings.TrimSpace(str(m, "name"))
		if st.fold {
			if d := dispatcherTool(m); d != nil && add(d, ns, true) {
				st.names[ns] = true
				children := map[string]struct{}{}
				for _, c := range childTools(m) {
					children[strings.TrimSpace(str(c, "name"))] = struct{}{}
				}
				st.refs[ns] = &nsRef{namespace: ns, dispatcher: true, children: children}
			}
			continue
		}
		for _, c := range childTools(m) {
			child := strings.TrimSpace(str(c, "name"))
			q := qualifyName(ns, child)
			if isTool(c) {
				c["name"] = q
				simplifyAutomation(c, ns, child)
			}
			if !add(c, q, false) || !isTool(c) {
				continue
			}
			st.names[q] = true
			if !claimed[q] && st.refs[q] == nil {
				st.refs[q] = &nsRef{namespace: ns, name: child}
			}
		}
	}
	// The folded namespaces stay when the tools are cut to maxTools.
	result := make([]any, 0, len(out))
	if len(out) > maxTools {
		for _, e := range out {
			if e.dispatcher {
				result = append(result, e.tool)
			}
		}
	}
	for _, e := range out {
		if len(out) <= maxTools || !e.dispatcher {
			result = append(result, e.tool)
		}
	}
	return result, st
}

// childTools returns the tools of a namespace, without the tool_search ones and the unnamed ones.
func childTools(ns map[string]any) []map[string]any {
	var out []map[string]any
	children, _ := ns["tools"].([]any)
	for _, c := range children {
		if cm, ok := c.(map[string]any); ok && cm["type"] != "tool_search" && (!isTool(cm) || strings.TrimSpace(str(cm, "name")) != "") {
			out = append(out, cm)
		}
	}
	return out
}

// dispatcherTool is the one function that stands for a folded namespace.
func dispatcherTool(ns map[string]any) map[string]any {
	name := strings.TrimSpace(str(ns, "name"))
	if name == "" {
		return nil
	}
	description := strings.TrimSpace(str(ns, "description"))
	var names, entries []string
	seen := map[string]bool{}
	for _, c := range childTools(ns) {
		child := strings.TrimSpace(str(c, "name"))
		if child == "" || seen[child] {
			continue
		}
		seen[child] = true
		names = append(names, child)
		entry := "- " + child
		if d := strings.TrimSpace(str(c, "description")); d != "" {
			entry += ": " + d
		}
		params, ok := c["parameters"].(map[string]any)
		if !ok {
			params, ok = c["input_schema"].(map[string]any)
		}
		if ok && len(params) > 0 && !isEmptyObjectSchema(params) {
			holder := map[string]any{"s": params}
			normalizeSchema(holder, "s")
			if b, err := marshal(holder["s"].(map[string]any)); err == nil {
				entry += "\n  Parameters: " + string(b)
			}
		}
		entries = append(entries, entry)
	}
	full := description
	switch {
	case len(entries) > 0:
		catalog := "Available tools in this namespace:\n" + strings.Join(entries, "\n")
		if full != "" {
			full += "\n\n" + catalog
		} else {
			full = fmt.Sprintf("Tools in namespace %s.\n\n%s", name, catalog)
		}
	case full == "":
		full = fmt.Sprintf("Tools in namespace %s.", name)
	}
	nameProp := map[string]any{"type": "string", "description": "Child tool name to execute in namespace " + name}
	if len(names) > 0 {
		nameProp["enum"] = names
	}
	return map[string]any{"type": "function", "name": name, "description": full, "parameters": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":      nameProp,
			"arguments": map[string]any{"type": "object", "description": "Arguments object matching the parameter schema of the selected child tool", "additionalProperties": true},
		},
		"required": []any{"name"},
	}}
}

func isEmptyObjectSchema(p map[string]any) bool {
	props, ok := p["properties"].(map[string]any)
	return len(p) == 2 && p["type"] == "object" && ok && len(props) == 0
}

// The Codex app's automation_update tool has a large oneOf schema with $ref references; with it
// xAI accepts the request and sends no events. Its parameters are replaced by one that accepts any
// object, as today's core replaces them.
const permissiveParameters = `{"type":"object","properties":{},"additionalProperties":true}`

func isAutomationUpdate(name, namespace string) bool {
	namespace = strings.TrimPrefix(strings.TrimSpace(namespace), "mcp__")
	name = strings.TrimPrefix(strings.TrimSpace(name), "mcp__")
	return strings.EqualFold(name, "automation_update") && (strings.EqualFold(namespace, "codex_app") || strings.EqualFold(namespace, "codex_apps")) ||
		strings.EqualFold(name, "codex_app__automation_update") || strings.EqualFold(name, "codex_apps__automation_update")
}

// simplifyAutomation gives a function that is the Codex app's automation_update the permissive
// parameters. namespace and name are the ones the client declared.
func simplifyAutomation(tool map[string]any, namespace, name string) {
	if (tool["type"] != "function" && tool["type"] != "custom") || !isAutomationUpdate(name, namespace) {
		return
	}
	var params any
	_ = json.Unmarshal([]byte(permissiveParameters), &params)
	tool["parameters"] = params
	if strict, _ := tool["strict"].(bool); strict {
		tool["strict"] = false
	}
}

// rewriteChoice names a function choice that has a namespace the way the tools were sent.
func (st *namespaceTools) rewriteChoice(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if ns := strings.TrimSpace(str(m, "namespace")); m["type"] == "function" && ns != "" {
		m["name"] = st.sentName(ns, str(m, "name"))
		delete(m, "namespace")
	}
	if list, ok := m["tools"].([]any); ok {
		for _, c := range list {
			st.rewriteChoice(c)
		}
	}
}

// sentName is the name a namespace's child is called by on xAI: the namespace's own when it was
// folded, the qualified name when it was flattened.
func (st *namespaceTools) sentName(namespace, child string) string {
	q := qualifyName(namespace, child)
	switch {
	case st.names[strings.TrimSpace(namespace)]:
		return strings.TrimSpace(namespace)
	case st.names[q]:
		return q
	case st.fold:
		return strings.TrimSpace(namespace)
	}
	return q
}

// rewriteHistory writes the function calls of the history that name a namespace the way the tools
// were sent: a call of a folded namespace goes to its dispatcher.
func (st *namespaceTools) rewriteHistory(input []any) {
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "function_call" {
			continue
		}
		ns := strings.TrimSpace(str(item, "namespace"))
		if ns == "" {
			continue
		}
		child := strings.TrimSpace(str(item, "name"))
		q := qualifyName(ns, child)
		if st.names[ns] || (!st.names[q] && st.fold) {
			wrapper := map[string]any{"name": child}
			if args := str(item, "arguments"); args != "" {
				if json.Valid([]byte(args)) {
					wrapper["arguments"] = json.RawMessage(args)
				} else {
					wrapper["arguments"] = args
				}
			}
			b, _ := marshal(wrapper)
			item["name"], item["arguments"] = ns, string(b)
		} else if q != "" {
			item["name"] = q
		}
		delete(item, "namespace")
	}
}

// nsRestorer turns xAI's calls to the functions that stand for namespace tools back into calls
// to the tools in their namespaces, in the events a Responses client is sent. It changes events
// one for one.
type nsRestorer struct {
	refs       map[string]*nsRef
	open       map[int]bool      // model item indexes of calls to restore
	dispatched map[string]string // item ID of a call to a folded namespace -> namespace
}

func newNSRestorer(refs map[string]*nsRef) *nsRestorer {
	return &nsRestorer{refs: refs, open: map[int]bool{}, dispatched: map[string]string{}}
}

func (r *nsRestorer) Transform(ev llm.Event) (llm.Event, error) {
	switch ev.Type {
	case llm.EventItemStart:
		if ev.Item != nil && ev.Item.Kind == llm.ItemToolCall && r.refs[strings.TrimSpace(ev.Item.Name)] != nil && len(ev.Raw) > 0 {
			r.open[ev.Index] = true
			ev.Raw = r.rewrite(ev.Raw, func(m map[string]any) bool { return r.restoreItem(m["item"], true) })
		}
	case llm.EventItemEnd:
		if r.open[ev.Index] {
			delete(r.open, ev.Index)
			ev.Raw = r.rewrite(ev.Raw, func(m map[string]any) bool { return r.restoreItem(m["item"], false) })
		}
	case llm.EventNative:
		switch {
		case bytes.Contains(ev.Raw, []byte(`"response.function_call_arguments.done"`)):
			ev.Raw = r.rewrite(ev.Raw, func(m map[string]any) bool {
				ns, ok := r.dispatched[str(m, "item_id")]
				if !ok {
					return false
				}
				if _, args, ok := unwrapDispatcher(str(m, "arguments"), r.refs[ns]); ok {
					m["arguments"] = string(args)
					return true
				}
				return false
			})
		case bytes.Contains(ev.Raw, []byte(`"response.output_item.done"`)):
			ev.Raw = r.rewrite(ev.Raw, func(m map[string]any) bool { return r.restoreItem(m["item"], false) })
		}
	case llm.EventFinish:
		ev.Raw = r.rewrite(ev.Raw, func(m map[string]any) bool {
			resp := m
			if rr, ok := m["response"].(map[string]any); ok {
				resp = rr
			}
			changed := false
			out, _ := resp["output"].([]any)
			for _, it := range out {
				if r.restoreItem(it, false) {
					changed = true
				}
			}
			return changed
		})
	}
	return ev, nil
}

// rewrite applies fn to the event in raw and returns the changed event, or raw when fn changed
// nothing or raw is not an object.
func (r *nsRestorer) rewrite(raw json.RawMessage, fn func(map[string]any) bool) json.RawMessage {
	m, err := decodeObject(raw)
	if err != nil || !fn(m) {
		return raw
	}
	out, err := marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// restoreItem changes a function call item in place. added is true for the added event, whose
// arguments have not come yet.
func (r *nsRestorer) restoreItem(v any, added bool) bool {
	item, ok := v.(map[string]any)
	if !ok || item["type"] != "function_call" {
		return false
	}
	ref := r.refs[strings.TrimSpace(str(item, "name"))]
	if ref == nil {
		return false
	}
	if !ref.dispatcher {
		item["name"], item["namespace"] = ref.name, ref.namespace
		return true
	}
	if id := str(item, "id"); id != "" {
		r.dispatched[id] = ref.namespace
	}
	item["namespace"] = ref.namespace
	if !added {
		if child, args, ok := unwrapDispatcher(str(item, "arguments"), ref); ok {
			item["name"], item["arguments"] = child, string(args)
		}
	}
	return true
}

// unwrapDispatcher reads the call a folded namespace's function was given: the child's name and
// arguments. It is not a call to a child when the arguments are not an object with a name, or the
// name is not one of the namespace's.
func unwrapDispatcher(arguments string, ref *nsRef) (string, []byte, bool) {
	var w struct {
		Name      any             `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if json.Unmarshal([]byte(arguments), &w) != nil {
		return "", nil, false
	}
	name, _ := w.Name.(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, false
	}
	if ref != nil && len(ref.children) > 0 {
		if _, ok := ref.children[name]; !ok {
			return "", nil, false
		}
	}
	args := bytes.TrimSpace(w.Arguments)
	switch {
	case len(args) == 0:
		m, err := decodeObject([]byte(arguments))
		if err != nil {
			return "", nil, false
		}
		delete(m, "name")
		if len(m) == 0 {
			return name, []byte("{}"), true
		}
		out, _ := marshal(m)
		return name, out, true
	case args[0] == '"':
		var s string
		if json.Unmarshal(args, &s) != nil {
			return "", nil, false
		}
		return name, []byte(s), true
	}
	return name, args, true
}
