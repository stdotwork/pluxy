package corpus

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

// The probes below check the exceptions, not the new core: a corrupted observation must never
// pass because some exception happens to sit above the corrupted value.

// probeValue replaces a node. It is an object so it is never equal to anything a core says and
// no exception for a single value can excuse it.
var probeValue = map[string]any{"corpus-probe": true}

func cloneObservation(t testing.TB, o Observation) Observation {
	t.Helper()
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	c, err := decodeObservation(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// setAt replaces the value at segs in place; the parents must exist.
func setAt(t testing.TB, o Observation, segs []segment, v any) {
	t.Helper()
	var cur any = map[string]any(o)
	for i, s := range segs {
		last := i == len(segs)-1
		switch c := cur.(type) {
		case map[string]any:
			if last {
				c[s.key] = v
				return
			}
			cur = c[s.key]
		case []any:
			if last {
				c[s.index] = v
				return
			}
			cur = c[s.index]
		default:
			t.Fatalf("setAt %s: no container at segment %d", formatPath(segs), i)
		}
	}
}

// nodePaths lists the path of every value in o, containers before their children.
func nodePaths(o Observation) [][]segment {
	var out [][]segment
	var walk func(path []segment, v any)
	walk = func(path []segment, v any) {
		if len(path) > 0 {
			out = append(out, clonePath(path))
		}
		switch c := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(c))
			for k := range c {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(append(clonePath(path), segment{key: k, index: -1}), c[k])
			}
		case []any:
			for i, child := range c {
				walk(append(clonePath(path), segment{index: i}), child)
			}
		}
	}
	walk(nil, map[string]any(o))
	return out
}

// probeSurvivors replaces each value of got in turn with probeValue and returns the paths where
// the case still passed. A judged case must have none: every value, whatever the exceptions
// say, is either compared or pinned.
func probeSurvivors(t testing.TB, want, got Observation, caseEx, globalEx []Exception) []string {
	t.Helper()
	var out []string
	for _, segs := range nodePaths(got) {
		probe := cloneObservation(t, got)
		setAt(t, probe, segs, probeValue)
		if Judge(want, probe, caseEx, globalEx).OK() {
			out = append(out, formatPath(segs))
		}
	}
	return out
}

func loadCorpus(t *testing.T) (*Global, map[string]*Case) {
	t.Helper()
	global, err := LoadGlobal("cases")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := LoadCases("cases")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*Case{}
	for _, c := range cases {
		byName[c.Format+"/"+c.Provider+"/"+c.Name] = c
	}
	return global, byName
}

func recordedOf(t *testing.T, c *Case) Observation {
	t.Helper()
	o, err := decodeObservation(c.Recorded)
	if err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
	return o
}

// An arbitrary 200 reply in place of the whole client observation must fail every case. Before
// the fix it passed the cases with an exception on "client" (models-list, nonstream-malformed-body,
// legacy-completions).
func TestArbitraryReplyFailsEveryCase(t *testing.T) {
	global, cases := loadCorpus(t)
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ref := recordedOf(t, c)
			got := cloneObservation(t, ref)
			got["client"] = map[string]any{
				"status":  json.Number("200"),
				"headers": map[string]any{"Content-Type": "text/plain"},
				"body":    "an arbitrary reply",
			}
			if v := Judge(ref, got, c.Exceptions, global.Exceptions); v.OK() {
				t.Errorf("an arbitrary 200 reply passed the case")
			}
		})
	}
}

// Replacing one choice of a multi-choice reply with a string must fail.
func TestReplacedChoiceFails(t *testing.T) {
	global, cases := loadCorpus(t)
	c := cases["openai-chat/openai-compatible/nonstream-multiple-choices"]
	if c == nil {
		t.Fatal("case nonstream-multiple-choices is missing")
	}
	ref := recordedOf(t, c)
	got := cloneObservation(t, ref)
	segs, err := parsePath("client.body.choices[1]")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(got, "client.body.choices[1]").(map[string]any); !ok {
		t.Fatalf("the case has no second choice")
	}
	setAt(t, got, segs, "an arbitrary string")
	if v := Judge(ref, got, c.Exceptions, global.Exceptions); v.OK() {
		t.Errorf("a replaced choice passed the case")
	}
}

// Probe every value of every case's recorded observation against itself, with no exceptions:
// this checks the comparison. The exceptions are probed on the new core's real observations in
// TestCorpus (see probeCase).
func TestProbeWithoutExceptionsRejectsEverything(t *testing.T) {
	_, cases := loadCorpus(t)
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ref := recordedOf(t, c)
			if got := probeSurvivors(t, ref, ref, nil, nil); len(got) > 0 {
				t.Errorf("probes survived with no exceptions at: %s", strings.Join(got, ", "))
			}
		})
	}
}

// probeCase is run by TestCorpus on each case's real observations.
func probeCase(t *testing.T, want, got Observation, c *Case, global *Global) {
	t.Helper()
	if s := probeSurvivors(t, want, got, c.Exceptions, global.Exceptions); len(s) > 0 {
		t.Errorf("an exception hides a corrupted value: replacing %s still passes the case", strings.Join(s, "; replacing "))
	}
}
