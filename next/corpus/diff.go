package corpus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Observation paths name a value inside an observation: keys joined by ".", array indexes in
// brackets, for example client.events[2].data.choices[0].delta or upstream[0].headers.Accept.
// In exceptions and volatile lists a segment may be "*" (any key) or "[*]" (any index).

type segment struct {
	key   string
	index int // -1 for a key segment
}

func parsePath(p string) ([]segment, error) {
	if p == "" {
		return nil, fmt.Errorf("empty path")
	}
	var out []segment
	for _, part := range strings.Split(p, ".") {
		key, rest := part, ""
		if i := strings.IndexByte(part, '['); i >= 0 {
			key, rest = part[:i], part[i:]
		}
		if key == "" && (len(out) == 0 || rest == "") {
			return nil, fmt.Errorf("empty key in path")
		}
		if key != "" {
			out = append(out, segment{key: key, index: -1})
		}
		// rest is a run of [n] or [*] groups.
		for rest != "" {
			if rest[0] != '[' {
				return nil, fmt.Errorf("unexpected %q after ]", rest)
			}
			idx, after, ok := strings.Cut(rest[1:], "]")
			if !ok {
				return nil, fmt.Errorf("unclosed [")
			}
			if idx == "*" {
				out = append(out, segment{key: "*", index: -2})
			} else {
				n, err := strconv.Atoi(idx)
				if err != nil || n < 0 {
					return nil, fmt.Errorf("bad index %q", idx)
				}
				out = append(out, segment{index: n})
			}
			rest = after
		}
	}
	return out, nil
}

func formatPath(segs []segment) string {
	var b strings.Builder
	for i, s := range segs {
		switch {
		case s.index >= 0:
			fmt.Fprintf(&b, "[%d]", s.index)
		case s.index == -2:
			b.WriteString("[*]")
		default:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(s.key)
		}
	}
	return b.String()
}

// covers reports whether pattern names path or one of its parents.
func covers(pattern, path []segment) bool {
	if len(pattern) > len(path) {
		return false
	}
	for i, p := range pattern {
		s := path[i]
		switch {
		case p.index == -2:
			if s.index < 0 {
				return false
			}
		case p.index >= 0:
			if s.index != p.index {
				return false
			}
		case p.key == "*":
			if s.index != -1 {
				return false
			}
		default:
			if s.index != -1 || !keyMatch(path, i, p.key) {
				return false
			}
		}
	}
	return true
}

// keyMatch reports whether path[i] is the key k. Header names compare case-insensitively.
func keyMatch(path []segment, i int, k string) bool {
	if path[i].key == k {
		return true
	}
	return i > 0 && path[i-1].index == -1 && path[i-1].key == "headers" && strings.EqualFold(path[i].key, k)
}

// Difference is one value that differs between two observations.
type Difference struct {
	Path string
	Want any // reference (old core); missing when absent
	Got  any // new core; missing when absent
	segs []segment
}

// missing marks a value that is absent on one side.
type missingValue struct{}

func (missingValue) String() string { return "(absent)" }

var missing = missingValue{}

// Diff lists every leaf where got differs from want.
func Diff(want, got any) []Difference {
	var out []Difference
	diffValue(nil, want, got, &out)
	return out
}

func diffValue(path []segment, want, got any, out *[]Difference) {
	wm, wok := want.(map[string]any)
	gm, gok := got.(map[string]any)
	if wok && gok {
		keys := map[string]bool{}
		for k := range wm {
			keys[k] = true
		}
		for k := range gm {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			wv, wok := wm[k]
			gv, gok := gm[k]
			if !wok {
				wv = missing
			}
			if !gok {
				gv = missing
			}
			diffValue(append(clonePath(path), segment{key: k, index: -1}), wv, gv, out)
		}
		return
	}
	wa, wok := want.([]any)
	ga, gok := got.([]any)
	if wok && gok {
		n := max(len(wa), len(ga))
		for i := 0; i < n; i++ {
			var wv, gv any = missing, missing
			if i < len(wa) {
				wv = wa[i]
			}
			if i < len(ga) {
				gv = ga[i]
			}
			diffValue(append(clonePath(path), segment{index: i}), wv, gv, out)
		}
		return
	}
	if !reflect.DeepEqual(want, got) {
		*out = append(*out, Difference{Path: formatPath(path), Want: want, Got: got, segs: path})
	}
}

func clonePath(p []segment) []segment { return append([]segment(nil), p...) }

// Mask replaces the values at the given paths with "(volatile)" in place.
func Mask(o Observation, patterns []string) {
	for _, p := range patterns {
		segs, err := parsePath(p)
		if err != nil {
			continue
		}
		maskAt(map[string]any(o), segs)
	}
}

func maskAt(v any, segs []segment) {
	if len(segs) == 0 {
		return
	}
	s, last := segs[0], len(segs) == 1
	switch c := v.(type) {
	case map[string]any:
		for k, child := range c {
			if s.index != -1 || (s.key != "*" && k != s.key && !strings.EqualFold(k, s.key)) {
				continue
			}
			if last {
				c[k] = "(volatile)"
			} else {
				maskAt(child, segs[1:])
			}
		}
	case []any:
		for i, child := range c {
			if s.index != -2 && s.index != i {
				continue
			}
			if last {
				c[i] = "(volatile)"
			} else {
				maskAt(child, segs[1:])
			}
		}
	}
}

// Lookup returns the value at path, or missing.
func Lookup(o Observation, path string) any {
	segs, err := parsePath(path)
	if err != nil {
		return missing
	}
	var v any = map[string]any(o)
	for _, s := range segs {
		switch c := v.(type) {
		case map[string]any:
			found := false
			for k, child := range c {
				if k == s.key || strings.EqualFold(k, s.key) {
					v, found = child, true
					break
				}
			}
			if !found {
				return missing
			}
		case []any:
			if s.index < 0 || s.index >= len(c) {
				return missing
			}
			v = c[s.index]
		default:
			return missing
		}
	}
	return v
}

// Verdict is the outcome of comparing the new core with the reference for one case.
type Verdict struct {
	// Covered differences, by the exception that covers them.
	Covered map[*Exception][]Difference
	// Uncovered differences fail the case.
	Uncovered []Difference
	// Stale exceptions cover no difference; they fail the case (case exceptions only).
	Stale []*Exception
	// WantFailed lists exceptions whose Want the new core does not give.
	WantFailed []WantFailure
}

// WantFailure is an exception whose Want value the new core does not give.
type WantFailure struct {
	Exception *Exception
	Got       any
}

// OK reports whether the case passes.
func (v Verdict) OK() bool {
	return len(v.Uncovered) == 0 && len(v.Stale) == 0 && len(v.WantFailed) == 0
}

// Judge compares got (new core) with want (reference) under the case's and the global
// exceptions. Global exceptions are never stale for a single case.
func Judge(want, got Observation, caseEx, globalEx []Exception) Verdict {
	v := Verdict{Covered: map[*Exception][]Difference{}}
	type compiled struct {
		ex     *Exception
		segs   []segment
		global bool
	}
	var all []compiled
	for i := range caseEx {
		segs, _ := parsePath(caseEx[i].Path)
		all = append(all, compiled{&caseEx[i], segs, false})
	}
	for i := range globalEx {
		segs, _ := parsePath(globalEx[i].Path)
		all = append(all, compiled{&globalEx[i], segs, true})
	}
	for _, d := range Diff(map[string]any(want), map[string]any(got)) {
		hit := false
		for _, c := range all {
			if covers(c.segs, d.segs) {
				v.Covered[c.ex] = append(v.Covered[c.ex], d)
				hit = true
				break
			}
		}
		if !hit {
			v.Uncovered = append(v.Uncovered, d)
		}
	}
	for _, c := range all {
		if !c.global && len(v.Covered[c.ex]) == 0 {
			v.Stale = append(v.Stale, c.ex)
		}
		if len(c.ex.Want) > 0 {
			wantV, err := decodeValue(c.ex.Want)
			gotV := Lookup(got, c.ex.Path)
			if err != nil || !reflect.DeepEqual(wantV, gotV) {
				v.WantFailed = append(v.WantFailed, WantFailure{c.ex, gotV})
			}
		}
	}
	return v
}

func decodeValue(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	return v, err
}

// Show formats a value for a report line.
func Show(v any) string {
	if _, ok := v.(missingValue); ok {
		return "(absent)"
	}
	b := marshalPlain(v)
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}
