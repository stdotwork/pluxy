// Package payload applies payload rules to a JSON request body: the config `payload` section
// that sets, fills in or removes fields of the body an extension is about to send upstream.
//
// The rule types and their JSON names are the old core's, so config can decode straight into
// Rules. Apply is pure: it reads the rules and the body and returns the new body, so a caller
// applies it once, to the final upstream body, after everything else has touched it.
package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Rules is the payload config. Default and DefaultRaw write a field only when it is missing
// (the first matching rule wins); Override and OverrideRaw always write it (the last wins); the
// Raw lists take JSON text as the value; Filter removes fields.
type Rules struct {
	Default     []Rule       `json:"default"`
	DefaultRaw  []Rule       `json:"default-raw"`
	Override    []Rule       `json:"override"`
	OverrideRaw []Rule       `json:"override-raw"`
	Filter      []FilterRule `json:"filter"`
}

// Rule writes Params, a path-to-value map, into bodies of the models it names. In the Raw
// lists a string value is JSON text, written as it is.
type Rule struct {
	Models []Model        `json:"models"`
	Params map[string]any `json:"params"`
}

// FilterRule removes the Params paths from bodies of the models it names.
type FilterRule struct {
	Models []Model  `json:"models"`
	Params []string `json:"params"`
}

// Model selects the requests a rule applies to. A rule applies when any of its models does.
type Model struct {
	// Name is the model name; `*` matches any run of characters.
	Name string `json:"name"`
	// Protocol restricts the rule to one extension channel (case-insensitive).
	Protocol string `json:"protocol"`
	// Headers requires every named client header to have a value matching its pattern.
	Headers map[string]string `json:"headers"`
	// FromProtocol restricts the rule to one client format.
	FromProtocol string `json:"from-protocol"`
	// Match requires each path to equal its value; NotMatch requires that none does.
	Match    []map[string]any `json:"match"`
	NotMatch []map[string]any `json:"not-match"`
	// Exist requires each path to exist and not be null; NotExist, that none does.
	Exist    []string `json:"exist"`
	NotExist []string `json:"not-exist"`
}

// Input is what a call knows besides the rules and the body.
type Input struct {
	// Model is the upstream model name and RequestedModel the name the client asked for. A
	// rule's model name may match either; a thinking suffix `name(8192)` on the requested one
	// also matches by its base name. Both empty applies no rule.
	Model, RequestedModel string
	// Protocol is the extension channel (`codex`, `claude`, ...); empty matches every rule.
	Protocol string
	// FromProtocol is the client format; a rule that names one needs it to match.
	FromProtocol string
	// Headers are the client's request headers.
	Headers http.Header
	// Root is a path prefix for every rule path, for bodies that carry the request inside
	// another object (for example `request`).
	Root string
	// Original is the body as the client sent it. Default rules check it, not the body being
	// built, for whether a field is missing, so a field the pipeline added is still defaulted.
	// Without it they check the body.
	Original []byte
}

// Apply returns the body with the rules applied, and whether it changed. A body that is not a
// JSON object or array is returned as it is. Untouched parts keep their bytes. Rule paths and
// raw values that Validate refuses are skipped.
func Apply(r *Rules, body []byte, in Input) ([]byte, bool) {
	if r == nil || len(body) == 0 || r.empty() {
		return body, false
	}
	model, requested := strings.TrimSpace(in.Model), strings.TrimSpace(in.RequestedModel)
	if model == "" && requested == "" {
		return body, false
	}
	if i := skipSpace(body, 0); i >= len(body) || (body[i] != '{' && body[i] != '[') || !json.Valid(body) {
		return body, false
	}
	e := &engine{body: body, in: in, candidates: modelCandidates(model, requested), applied: map[string]struct{}{}}
	if len(in.Original) > 0 {
		e.original = in.Original
		e.originalOK = json.Valid(in.Original)
	}
	e.run(r)
	return e.body, e.changed
}

func (r *Rules) empty() bool {
	return len(r.Default) == 0 && len(r.DefaultRaw) == 0 && len(r.Override) == 0 && len(r.OverrideRaw) == 0 && len(r.Filter) == 0
}

type engine struct {
	body       []byte
	changed    bool
	in         Input
	candidates []string
	original   []byte
	originalOK bool // original is valid JSON; if not, no path exists in it

	// Default rules: first write wins, and a field a default created does not count as one the
	// client sent.
	applied      map[string]struct{}
	created      map[string]struct{}
	initialItems map[string]int
}

func (e *engine) run(r *Rules) {
	// Params is a map; its paths go in sorted order so a result never depends on map order.
	for i := range r.Default {
		rule := &r.Default[i]
		if !e.matches(rule.Models) {
			continue
		}
		for _, p := range sortedKeys(rule.Params) {
			e.writeDefault(p, rule.Params[p], false)
		}
	}
	for i := range r.DefaultRaw {
		rule := &r.DefaultRaw[i]
		if !e.matches(rule.Models) {
			continue
		}
		for _, p := range sortedKeys(rule.Params) {
			e.writeDefault(p, rule.Params[p], true)
		}
	}
	for i := range r.Override {
		rule := &r.Override[i]
		if !e.matches(rule.Models) {
			continue
		}
		for _, p := range sortedKeys(rule.Params) {
			for _, comps := range e.resolve(e.fullPath(p)) {
				e.override(comps, rule.Params[p])
			}
		}
	}
	for i := range r.OverrideRaw {
		rule := &r.OverrideRaw[i]
		if !e.matches(rule.Models) {
			continue
		}
		for _, p := range sortedKeys(rule.Params) {
			raw, ok := rawValue(rule.Params[p])
			if !ok {
				continue
			}
			for _, comps := range e.resolve(e.fullPath(p)) {
				e.overrideRaw(comps, raw)
			}
		}
	}
	for i := range r.Filter {
		rule := &r.Filter[i]
		if !e.matches(rule.Models) {
			continue
		}
		for _, p := range rule.Params {
			resolved := e.resolve(e.fullPath(p))
			// Last first, so removing an array element leaves the earlier indexes valid.
			for k := len(resolved) - 1; k >= 0; k-- {
				if out, ok := deleteValue(e.body, resolved[k]); ok {
					e.body, e.changed = out, true
				}
			}
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (e *engine) fullPath(path string) string {
	root, path := strings.TrimSpace(e.in.Root), strings.TrimSpace(path)
	switch {
	case root == "":
		return path
	case path == "":
		return root
	}
	return root + "." + strings.TrimPrefix(path, ".")
}

func (e *engine) write(comps []string, raw []byte) bool {
	out, err := setValue(e.body, comps, raw)
	if err != nil {
		return false
	}
	e.body, e.changed = out, true
	return true
}

func pathKey(comps []string) string { return strings.Join(comps, "\x00") }

// writeDefault sets the field of each path the param resolves to when the source has no such
// field and no earlier default wrote it.
func (e *engine) writeDefault(path string, v any, raw bool) {
	for _, comps := range e.resolve(e.fullPath(path)) {
		if e.sourceHas(comps) {
			continue
		}
		if _, done := e.applied[pathKey(comps)]; done {
			continue
		}
		var text []byte
		var ok bool
		if raw {
			if text, ok = rawValue(v); !ok {
				continue
			}
		} else {
			var err error
			if text, err = encodeValue(v); err != nil {
				continue
			}
		}
		e.recordCreation(comps)
		if e.write(comps, text) {
			e.applied[pathKey(comps)] = struct{}{}
		}
	}
}

// sourceHas reports whether the field exists in the body the client sent (Original) or, without
// one, in the body now, not counting what a default rule created.
func (e *engine) sourceHas(comps []string) bool {
	if len(e.original) > 0 {
		return e.originalOK && lookup(e.original, comps).ok
	}
	if _, made := e.created[pathKey(comps)]; made {
		return false
	}
	for k := 1; k < len(comps); k++ {
		prefix := pathKey(comps[:k])
		if _, made := e.created[prefix]; made {
			return false
		}
		if n, ok := e.initialItems[prefix]; ok {
			if idx, err := strconv.Atoi(comps[k]); err == nil && idx >= n {
				return false
			}
		}
	}
	return lookup(e.body, comps).ok
}

// recordCreation notes, before a default writes comps, which of its parents and itself the body
// does not have yet, and how long the arrays above it are.
func (e *engine) recordCreation(comps []string) {
	if len(e.original) > 0 {
		return
	}
	if e.created == nil {
		e.created, e.initialItems = map[string]struct{}{}, map[string]int{}
	}
	for k := 1; k < len(comps); k++ {
		key := pathKey(comps[:k])
		v := lookup(e.body, comps[:k])
		switch {
		case !v.ok:
			e.created[key] = struct{}{}
		case v.raw[0] == '[':
			if _, seen := e.initialItems[key]; !seen {
				e.initialItems[key] = len(elements(v.raw))
			}
		}
	}
	if !lookup(e.body, comps).ok {
		e.created[pathKey(comps)] = struct{}{}
	}
}

// override writes v at comps unless the field already holds it.
func (e *engine) override(comps []string, v any) {
	cur := lookup(e.body, comps)
	switch want := v.(type) {
	case string:
		var have string
		if cur.ok && cur.raw[0] == '"' && json.Unmarshal(cur.raw, &have) == nil && have == want {
			return
		}
	case bool:
		if cur.ok && string(cur.raw) == strconv.FormatBool(want) {
			return
		}
	case nil:
		if cur.ok && cur.null() {
			return
		}
	default:
		if text, err := encodeValue(v); err == nil && cur.ok && bytes.Equal(cur.raw, text) {
			return
		}
	}
	if text, err := encodeValue(v); err == nil {
		e.write(comps, text)
	}
}

func (e *engine) overrideRaw(comps []string, raw []byte) {
	if cur := lookup(e.body, comps); cur.ok && bytes.Equal(cur.raw, raw) {
		return
	}
	e.write(comps, raw)
}

// rawValue returns the JSON text of a Raw rule's value: a string is the text itself, anything
// else is encoded. It reports false for a value that is not valid JSON.
func rawValue(v any) ([]byte, bool) {
	var raw []byte
	switch v := v.(type) {
	case nil:
		return nil, false
	case string:
		raw = []byte(v)
	case []byte:
		raw = v
	default:
		var err error
		if raw, err = json.Marshal(v); err != nil {
			return nil, false
		}
	}
	if !json.Valid(raw) {
		return nil, false
	}
	return raw, true
}

// resolve turns a rule path into the plain paths it names in the body: a key or index names
// itself, and a query names the index of the first (or every) matching element of the array
// before it. A query with no array before it, or no match, names nothing.
func (e *engine) resolve(path string) [][]string {
	parts, err := parsePath(path)
	if err != nil {
		return nil
	}
	paths := [][]string{nil}
	for _, p := range parts {
		if p.query == nil {
			for i := range paths {
				paths[i] = append(paths[i][:len(paths[i]):len(paths[i])], p.key)
			}
			continue
		}
		var next [][]string
		for _, base := range paths {
			arr := lookup(e.body, base)
			if !arr.ok || arr.raw[0] != '[' {
				continue
			}
			for idx, item := range elements(arr.raw) {
				if !p.matches(item) {
					continue
				}
				next = append(next, append(base[:len(base):len(base)], strconv.Itoa(idx)))
				if !p.all {
					break
				}
			}
		}
		if paths = next; len(paths) == 0 {
			return nil
		}
	}
	return paths
}

// modelCandidates lists the names a rule's model name is tried against: the upstream model, the
// requested model without a thinking suffix `(...)`, and the requested model as asked. Names are
// compared exactly, as rule names are, so a name that differs in case is kept.
func modelCandidates(model, requested string) []string {
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || slices.Contains(out, s) {
			return
		}
		out = append(out, s)
	}
	add(model)
	if requested != "" {
		base, suffixed := requested, false
		if open := strings.LastIndex(requested, "("); open >= 0 && strings.HasSuffix(requested, ")") {
			base, suffixed = requested[:open], true
		}
		add(base)
		if suffixed {
			add(requested)
		}
	}
	return out
}

// matches reports whether any model entry applies to this call and to the body as it is now.
func (e *engine) matches(models []Model) bool {
	if len(models) == 0 {
		return false
	}
	for _, name := range e.candidates {
		for i := range models {
			m := &models[i]
			pattern := strings.TrimSpace(m.Name)
			if pattern == "" {
				continue
			}
			if p := strings.TrimSpace(m.Protocol); p != "" && e.in.Protocol != "" && !strings.EqualFold(p, e.in.Protocol) {
				continue
			}
			if !fromProtocolMatches(m.FromProtocol, e.in.FromProtocol) || !headersMatch(e.in.Headers, m.Headers) {
				continue
			}
			if matchPattern(pattern, name) && e.conditionsMatch(m) {
				return true
			}
		}
	}
	return false
}

func (e *engine) conditionsMatch(m *Model) bool {
	for _, cond := range m.Match {
		for path, want := range cond {
			if strings.TrimSpace(path) != "" && !e.pathEquals(path, want) {
				return false
			}
		}
	}
	for _, cond := range m.NotMatch {
		for path, want := range cond {
			if strings.TrimSpace(path) != "" && e.pathEquals(path, want) {
				return false
			}
		}
	}
	for _, path := range m.Exist {
		if strings.TrimSpace(path) != "" && !e.pathExists(path) {
			return false
		}
	}
	for _, path := range m.NotExist {
		if strings.TrimSpace(path) != "" && e.pathExists(path) {
			return false
		}
	}
	return true
}

// pathExists reports whether any path the rule path resolves to holds a value that is not null.
func (e *engine) pathExists(path string) bool {
	for _, comps := range e.resolve(e.fullPath(path)) {
		if v := lookup(e.body, comps); v.ok && !v.null() {
			return true
		}
	}
	return false
}

// pathEquals reports whether any path the rule path resolves to holds a value equal to want,
// compared as decoded JSON (so 1 equals 1.0 and key order does not matter).
func (e *engine) pathEquals(path string, want any) bool {
	var expected any
	text, err := json.Marshal(want)
	if err != nil || json.Unmarshal(text, &expected) != nil {
		return false
	}
	for _, comps := range e.resolve(e.fullPath(path)) {
		v := lookup(e.body, comps)
		var actual any
		if v.ok && json.Unmarshal(v.raw, &actual) == nil && reflect.DeepEqual(actual, expected) {
			return true
		}
	}
	return false
}

func fromProtocolMatches(pattern, got string) bool {
	pattern = normalizeFormat(pattern)
	if pattern == "" {
		return true
	}
	got = normalizeFormat(got)
	return got != "" && strings.EqualFold(pattern, got)
}

func normalizeFormat(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "openai-response", "openai-responses", "response":
		return "responses"
	}
	return s
}

// headersMatch reports whether every header the rule names has a value matching its pattern.
func headersMatch(h http.Header, rules map[string]string) bool {
	for key, pattern := range rules {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		found := false
		for k, values := range h {
			if !strings.EqualFold(k, key) {
				continue
			}
			for _, v := range values {
				if matchPattern(pattern, v) {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// matchPattern is a glob where `*` matches any run of characters (including none) and every
// other character stands for itself, so a `*` in the pattern is always a wildcard, also when the
// value has a `*` there. Both sides are trimmed, and an empty pattern matches nothing.
//
// The pattern is split at its stars: the first piece must start the value, the last must end it,
// and the pieces between are found in order, so the work stays linear in the two lengths.
func matchPattern(pattern, s string) bool {
	pattern, s = strings.TrimSpace(pattern), strings.TrimSpace(s)
	if pattern == "" {
		return false
	}
	pieces := strings.Split(pattern, "*")
	if len(pieces) == 1 {
		return pattern == s
	}
	first, last := pieces[0], pieces[len(pieces)-1]
	if len(s) < len(first)+len(last) || !strings.HasPrefix(s, first) || !strings.HasSuffix(s, last) {
		return false
	}
	s = s[len(first) : len(s)-len(last)]
	for _, piece := range pieces[1 : len(pieces)-1] {
		at := strings.Index(s, piece)
		if at < 0 {
			return false
		}
		s = s[at+len(piece):]
	}
	return true
}

// Validate checks the rules the way a config loader should before it keeps them: every path
// (rule params, filter params, match, not-match, exist, not-exist) must be inside the supported
// syntax, and every raw value must be valid JSON. The error lists every problem, each naming the
// rule and the path or value.
func Validate(r *Rules) error {
	if r == nil {
		return nil
	}
	var errs []error
	for _, s := range []struct {
		name  string
		rules []Rule
		raw   bool
	}{{"default", r.Default, false}, {"default-raw", r.DefaultRaw, true}, {"override", r.Override, false}, {"override-raw", r.OverrideRaw, true}} {
		for i, rule := range s.rules {
			where := fmt.Sprintf("payload.%s[%d]", s.name, i)
			errs = append(errs, validateModels(where, rule.Models)...)
			for _, p := range sortedKeys(rule.Params) {
				errs = append(errs, validatePath(where+".params", p)...)
				if !s.raw {
					continue
				}
				if _, ok := rawValue(rule.Params[p]); !ok {
					errs = append(errs, fmt.Errorf("%s.params[%q]: the raw value is not valid JSON", where, p))
				}
			}
		}
	}
	for i, rule := range r.Filter {
		where := fmt.Sprintf("payload.filter[%d]", i)
		errs = append(errs, validateModels(where, rule.Models)...)
		for _, p := range rule.Params {
			errs = append(errs, validatePath(where+".params", p)...)
		}
	}
	return errors.Join(errs...)
}

func validateModels(where string, models []Model) []error {
	var errs []error
	for i, m := range models {
		where := fmt.Sprintf("%s.models[%d]", where, i)
		for _, conds := range [][]map[string]any{m.Match, m.NotMatch} {
			for _, cond := range conds {
				for _, p := range sortedKeys(cond) {
					errs = append(errs, validatePath(where, p)...)
				}
			}
		}
		for _, paths := range [][]string{m.Exist, m.NotExist} {
			for _, p := range paths {
				errs = append(errs, validatePath(where, p)...)
			}
		}
	}
	return errs
}

// validatePath checks one rule path. A blank path is skipped, as Apply skips it.
func validatePath(where, path string) []error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if _, err := parsePath(path); err != nil {
		return []error{fmt.Errorf("%s: path %q: %w", where, path, err)}
	}
	return nil
}
