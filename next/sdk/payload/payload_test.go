package payload

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// testdata/cases.json holds rules, inputs and bodies together with what the old core's payload
// engine returned for them (the "old" field). testdata/oldengine_gen.go.txt says how it was made.
type oldCase struct {
	Name  string `json:"name"`
	Rules Rules  `json:"rules"`
	Input struct {
		Model          string              `json:"model"`
		RequestedModel string              `json:"requested-model"`
		Protocol       string              `json:"protocol"`
		FromProtocol   string              `json:"from-protocol"`
		Root           string              `json:"root"`
		Headers        map[string][]string `json:"headers"`
		Original       *string             `json:"original"`
	} `json:"input"`
	Body string `json:"body"`
	Old  string `json:"old"`
}

func loadCases(t *testing.T) []oldCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []oldCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	return cases
}

// Apply returns, byte for byte, what the old engine returned for the same rules and inputs.
func TestApplyMatchesOldEngine(t *testing.T) {
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			in := Input{
				Model: c.Input.Model, RequestedModel: c.Input.RequestedModel, Protocol: c.Input.Protocol,
				FromProtocol: c.Input.FromProtocol, Root: c.Input.Root, Headers: http.Header{},
			}
			for k, vs := range c.Input.Headers {
				for _, v := range vs {
					in.Headers.Add(k, v)
				}
			}
			if c.Input.Original != nil {
				in.Original = []byte(*c.Input.Original)
			}
			body := []byte(c.Body)
			got, changed := Apply(&c.Rules, body, in)
			if string(got) != c.Old {
				t.Errorf("body\n got: %s\nwant: %s", got, c.Old)
			}
			if want := c.Old != c.Body; changed != want {
				t.Errorf("changed = %v, want %v", changed, want)
			}
			if string(body) != c.Body {
				t.Errorf("Apply modified the body it was given")
			}
		})
	}
}

func TestValidateAcceptsTheConfigExamples(t *testing.T) {
	n := 0
	for _, c := range loadCases(t) {
		if !strings.HasPrefix(c.Name, "example ") {
			continue
		}
		n++
		if err := Validate(&c.Rules); err != nil {
			t.Errorf("%s: %v", c.Name, err)
		}
	}
	if n < 20 {
		t.Fatalf("only %d example cases", n)
	}
}

func TestValidateRefuses(t *testing.T) {
	model := []Model{{Name: "gpt-*"}}
	params := func(p string) *Rules { return &Rules{Override: []Rule{{Models: model, Params: map[string]any{p: 1}}}} }
	cases := []struct {
		name  string
		rules *Rules
		want  string // a part of the message; empty means accepted
	}{
		{"dotted keys", params("a.b.c"), ""},
		{"index", params("a.0.b"), ""},
		{"query", params(`tools.#(name=="x")`), ""},
		{"all matches query with a tail", params(`a.#(k=="v"&&n!=2||t==true)#.b`), ""},
		{"escaped dot", params(`a\.b`), ""},
		{"blank path is skipped", params("  "), ""},
		{"wildcard", params("a.*.b"), `path "a.*.b": '*' is not supported`},
		{"question mark", params("a?"), `'?' is not supported`},
		{"pipe", params("a|b"), `'|' is not supported`},
		{"modifier", params("a.@reverse"), `'@' is not supported`},
		{"count", params("a.#"), `'#' is not supported`},
		{"append", params("a.-1"), "-1 (append)"},
		{"empty part", params("a..b"), "a part of the path is empty"},
		{"trailing dot", params("a."), "a part of the path is empty"},
		{"leading dot", params(".a"), "a part of the path is empty"},
		{"multipath", params("a.[b,c]"), `may not start with '['`},
		{"literal", params("!a"), `may not start with '!'`},
		{"forced key", params(":0"), `may not start with ':'`},
		{"quoted key", params(`a."b"`), `'"' is not supported`},
		{"other escape", params(`a\*`), "a backslash may only escape a dot"},
		{"leading zero index", params("a.01"), "no leading zeros"},
		{"huge index", params("a.99999999999"), "at most"},
		{"index that overflows an int", params("items.99999999999999999999"), "at most"},
		{"unclosed query", params("a.#(b==1"), "a query is not closed"},
		{"text after a query", params(`a.#(b==1)x`), "only `#` may follow"},
		{"query without operator", params("a.#(b)"), "needs == or !="},
		{"query with an ordering operator", params("a.#(b>1)"), "only == and != are supported"},
		{"query with a pattern operator", params(`a.#(b%"x*")`), "only == and != are supported"},
		{"query with a bare word", params("a.#(b==x)"), "must be a JSON string, number, true or false"},
		{"query with null", params("a.#(b==null)"), "must be a JSON string, number, true or false"},
		{"query with a wildcard key", params("a.#(b*==1)"), "its key must be plain keys"},
		{"query with an empty term", params("a.#(b==1&&)"), "needs == or !="},
		{"query value is a bad string", params(`a.#(b=="x)`), "a query is not closed"},
		{"filter path", &Rules{Filter: []FilterRule{{Models: model, Params: []string{"ok", "a.*"}}}}, "payload.filter[0].params: path \"a.*\""},
		{"match path", &Rules{Default: []Rule{{Models: []Model{{Name: "x", Match: []map[string]any{{"a|b": 1}}}}}}}, "payload.default[0].models[0]: path \"a|b\""},
		{"not-match path", &Rules{Default: []Rule{{Models: []Model{{Name: "x", NotMatch: []map[string]any{{"a|b": 1}}}}}}}, "path \"a|b\""},
		{"exist path", &Rules{Default: []Rule{{Models: []Model{{Name: "x", Exist: []string{"a|b"}}}}}}, "path \"a|b\""},
		{"not-exist path", &Rules{Default: []Rule{{Models: []Model{{Name: "x", NotExist: []string{"a|b"}}}}}}, "path \"a|b\""},
		{"raw object text", &Rules{OverrideRaw: []Rule{{Models: model, Params: map[string]any{"a": `{"x":[1,2]}`}}}}, ""},
		{"raw text with spaces around", &Rules{DefaultRaw: []Rule{{Models: model, Params: map[string]any{"a": " 5 "}}}}, ""},
		{"raw value given as an object", &Rules{DefaultRaw: []Rule{{Models: model, Params: map[string]any{"a": map[string]any{"x": 1}}}}}, ""},
		{"raw text that is not JSON", &Rules{OverrideRaw: []Rule{{Models: model, Params: map[string]any{"a": `{"x":`}}}}, `payload.override-raw[0].params["a"]: the raw value is not valid JSON`},
		{"raw bare word", &Rules{DefaultRaw: []Rule{{Models: model, Params: map[string]any{"a": "hello"}}}}, `payload.default-raw[0].params["a"]: the raw value is not valid JSON`},
		{"raw empty text", &Rules{DefaultRaw: []Rule{{Models: model, Params: map[string]any{"a": "  "}}}}, "the raw value is not valid JSON"},
		{"raw null", &Rules{OverrideRaw: []Rule{{Models: model, Params: map[string]any{"a": nil}}}}, "the raw value is not valid JSON"},
		{"raw null text is valid JSON, as in the old engine", &Rules{OverrideRaw: []Rule{{Models: model, Params: map[string]any{"a": "null", "b": " null "}}}}, ""},
		{"plain value is not checked as JSON text", &Rules{Override: []Rule{{Models: model, Params: map[string]any{"a": "hello"}}}}, ""},
		{"nil rules", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(c.rules)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case c.want != "" && err == nil:
				t.Errorf("accepted; want an error with %q", c.want)
			case c.want != "" && !strings.Contains(err.Error(), c.want):
				t.Errorf("error %q does not contain %q", err, c.want)
			}
		})
	}
}

// Validate names every problem, not only the first.
func TestValidateListsEveryProblem(t *testing.T) {
	r := &Rules{
		Default:     []Rule{{Models: []Model{{Name: "x"}}, Params: map[string]any{"a*": 1}}},
		OverrideRaw: []Rule{{Models: []Model{{Name: "x"}}, Params: map[string]any{"ok": "1", "bad": "{"}}},
		Filter:      []FilterRule{{Models: []Model{{Name: "x"}}, Params: []string{"b|c"}}},
	}
	err := Validate(r)
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{`payload.default[0].params: path "a*"`, `payload.override-raw[0].params["bad"]`, `payload.filter[0].params: path "b|c"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), `"ok"`) {
		t.Errorf("error blames a valid raw value:\n%v", err)
	}
}

// A path Validate refuses, or a raw value that is not JSON, that reaches Apply anyway is skipped:
// the body stays valid JSON.
func TestApplySkipsWhatValidateRefuses(t *testing.T) {
	r := &Rules{
		Override:    []Rule{{Models: []Model{{Name: "*"}}, Params: map[string]any{"a.*": 1, "l.-1": 2, "x.#(b>1)": 3}}},
		OverrideRaw: []Rule{{Models: []Model{{Name: "*"}}, Params: map[string]any{"b": "{"}}},
		DefaultRaw:  []Rule{{Models: []Model{{Name: "*"}}, Params: map[string]any{"c": "nope"}}},
		Filter:      []FilterRule{{Models: []Model{{Name: "*"}}, Params: []string{"a.*", "a|b"}}},
	}
	body := []byte(`{"a":{"x":1},"l":[1],"x":[{"b":2}]}`)
	got, changed := Apply(r, body, Input{Model: "m"})
	if changed || !bytes.Equal(got, body) {
		t.Errorf("Apply changed the body: %s", got)
	}
}

func TestApplyEdges(t *testing.T) {
	r := &Rules{Override: []Rule{{Models: []Model{{Name: "*"}}, Params: map[string]any{"a": 1}}}}
	for _, c := range []struct {
		name string
		r    *Rules
		body string
	}{{"nil rules", nil, `{"a":0}`}, {"empty rules", &Rules{}, `{"a":0}`}, {"invalid JSON", r, `{"a":`}, {"scalar root", r, `7`}, {"empty body", r, ``}} {
		got, changed := Apply(c.r, []byte(c.body), Input{Model: "m"})
		if changed || string(got) != c.body {
			t.Errorf("%s: got %q, changed %v", c.name, got, changed)
		}
	}
	if got, changed := Apply(r, []byte(`{"a":0}`), Input{}); changed || string(got) != `{"a":0}` {
		t.Errorf("no model: got %q, changed %v", got, changed)
	}
}

func TestMatchPattern(t *testing.T) {
	long := func(n int, c string) string { return strings.Repeat(c, n) }
	for _, c := range []struct {
		pattern, s string
		want       bool
	}{
		// A `*` in the pattern is always a wildcard, also when the value has a `*` there.
		{"*", "*/*", true},
		{"*", "*", true},
		{"a*", "a*b", true},
		{"a*b", "a*b", true},
		{"*/*", "*/*", true},
		{"a*", "*a", false},
		{"*b", "a*", false},
		// The usual glob cases.
		{"gpt-*", "gpt-5", true},
		{"gpt-*", "gpt-", true},
		{"gpt-*", "gp", false},
		{"*-mini", "gpt-5-mini", true},
		{"*-mini", "gpt-5-minis", false},
		{"*gpt*", "my-gpt-5", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
		{"a*b*c", "aXbYcZ", false},
		{"a**b", "ab", true},
		{"**", "", true},
		{"*", "", true},
		{"exact", "exact", true},
		{"exact", "exacts", false},
		{"exact", "exac", false},
		{"EXACT", "exact", false},
		// The prefix and the suffix may not share characters.
		{"ab*ab", "ab", false},
		{"ab*ab", "aba", false},
		{"ab*ab", "abab", true},
		{"a*a", "a", false},
		{"a*a", "aa", true},
		{"*a*a*", "a", false},
		{"*a*a*", "aa", true},
		// Segments are found in order, each after the one before.
		{"*ab*ba*", "abba", true},
		{"*ab*ab*", "aab", false},
		{"*ab*ab*", "xabxab", true},
		// Both sides are trimmed, and an empty pattern matches nothing.
		{" gpt-* ", "  gpt-5 ", true},
		{"", "", false},
		{"  ", "x", false},
		{"", "x", false},
		// Multi-byte characters stand for themselves.
		{"é*", "éa", true},
		{"*é", "aé", true},
		{"*é", "ae", false},
		// A long pattern against a long value.
		{"*" + long(2000, "a") + "b", long(4000, "a"), false},
		{"*" + long(2000, "a") + "b*", long(4000, "a"), false},
		{"*" + long(2000, "a") + "b", long(4000, "a") + "b", true},
		{long(2000, "a*"), long(2000, "a"), true},
		{long(2000, "a*") + "b", long(2000, "a"), false},
	} {
		if got := matchPattern(c.pattern, c.s); got != c.want {
			t.Errorf("matchPattern(%.40q, %.40q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

// A client header can be as long as the server allows and the pattern comes from the config, so
// matching must stay about as fast as reading both. The backtracking matcher took seconds here.
func TestMatchPatternLongInputs(t *testing.T) {
	const k, n = 50000, 100000
	pat := "*" + strings.Repeat("a", k) + "b"
	value := strings.Repeat("a", n)
	done := make(chan bool, 1)
	go func() {
		done <- matchPattern(pat, value) || matchPattern(pat+"*", value)
	}()
	select {
	case got := <-done:
		if got {
			t.Error("matched")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("matchPattern did not finish within 3s on a long pattern and a long value")
	}
}

// The upstream model, the requested model and its base are all tried against the rule's name
// exactly: a name that differs only in case is another name.
func TestModelNamesAreComparedExactly(t *testing.T) {
	rules := func(name string) *Rules {
		return &Rules{Override: []Rule{{Models: []Model{{Name: name}}, Params: map[string]any{"x": 1}}}}
	}
	for _, c := range []struct {
		name       string
		in         Input
		rule       string
		wantChange bool
	}{
		{"requested differs in case", Input{Model: "abc", RequestedModel: "ABC"}, "ABC", true},
		{"upstream differs in case", Input{Model: "ABC", RequestedModel: "abc"}, "ABC", true},
		{"upstream matches", Input{Model: "abc", RequestedModel: "ABC"}, "abc", true},
		{"base of a suffixed request differs in case", Input{Model: "abc", RequestedModel: "ABC(8192)"}, "ABC", true},
		{"neither is the name", Input{Model: "abc", RequestedModel: "ABC"}, "Abc", false},
		{"case does not match", Input{Model: "abc"}, "ABC", false},
	} {
		got, changed := Apply(rules(c.rule), []byte(`{}`), c.in)
		if changed != c.wantChange {
			t.Errorf("%s: changed = %v, want %v (body %s)", c.name, changed, c.wantChange, got)
		}
	}
	if got := modelCandidates("abc", "ABC"); len(got) != 2 || got[0] != "abc" || got[1] != "ABC" {
		t.Errorf("modelCandidates = %q, want [abc ABC]", got)
	}
	if got := modelCandidates("abc", "abc"); len(got) != 1 {
		t.Errorf("modelCandidates of equal names = %q, want one", got)
	}
}

func TestArrayIndexOverflow(t *testing.T) {
	for _, c := range []struct {
		s    string
		want int
	}{{"0", 0}, {"42", 42}, {"9223372036854775807", math.MaxInt}, {"9223372036854775808", math.MaxInt}, {"99999999999999999999", math.MaxInt}, {strings.Repeat("9", 1000), math.MaxInt}} {
		if n, ok := arrayIndex(c.s); !ok || n != c.want {
			t.Errorf("arrayIndex(%.30q) = %d, %v; want %d, true", c.s, n, ok, c.want)
		}
	}
	if _, ok := arrayIndex("1a"); ok {
		t.Error("arrayIndex accepted a non-digit")
	}
}

// A rule path with an index that does not fit an int is skipped by Apply, in every list and in
// the conditions, without panicking, and the body is untouched.
func TestApplySkipsOverflowingIndex(t *testing.T) {
	const path = "items.99999999999999999999"
	models := []Model{{Name: "*"}}
	for name, r := range map[string]*Rules{
		"default":      {Default: []Rule{{Models: models, Params: map[string]any{path: 1}}}},
		"default-raw":  {DefaultRaw: []Rule{{Models: models, Params: map[string]any{path: "1"}}}},
		"override":     {Override: []Rule{{Models: models, Params: map[string]any{path: 1}}}},
		"override-raw": {OverrideRaw: []Rule{{Models: models, Params: map[string]any{path: "1"}}}},
		"filter":       {Filter: []FilterRule{{Models: models, Params: []string{path}}}},
		"exist":        {Default: []Rule{{Models: []Model{{Name: "*", NotExist: []string{path}}}, Params: map[string]any{"z": 1}}}},
	} {
		body := []byte(`{"items":[1,2,3]}`)
		got, changed := Apply(r, body, Input{Model: "m"})
		if name == "exist" {
			// The path names nothing, so not-exist holds and the rule applies.
			if want := `{"items":[1,2,3],"z":1}`; string(got) != want {
				t.Errorf("%s: got %s, want %s", name, got, want)
			}
			continue
		}
		if changed || !bytes.Equal(got, body) {
			t.Errorf("%s: Apply changed the body: %s", name, got)
		}
	}
}

// A lookup that reports a value always returns its text, so callers may read raw[0].
func TestLookupFoundHasText(t *testing.T) {
	bodies := []string{
		`{"a":1,"b":{"c":[1,{"d":null},"x",[],{}],"":2},"e":"","f":""}`,
		`[1,[2,3],{"a":[]}]`,
		` { "a" : [ 1 , 2 ] , "b" : { } } `,
		`{"a":}`, `{"a":,"b":1}`, `{"a" 1}`, `{"a":1,}`, `[,]`, `[1,]`, `[1 2]`, `{"a":[1,`, `{"a":"`, `{`, `[`, ``, `   `, `7`, `"s"`,
	}
	paths := [][]string{
		{"a"}, {"b"}, {"b", "c"}, {"b", "c", "0"}, {"b", "c", "1", "d"}, {"b", "c", "2"}, {"b", "c", "3"}, {"b", "c", "4"}, {"b", ""}, {"e"}, {"f"},
		{"0"}, {"1"}, {"1", "0"}, {"2", "a"}, {"2", "a", "0"}, {"a", "0"}, {"a", "1"}, {"a", "2"}, {"b", "0"},
		{"a", "99999999999999999999"}, {""}, {"x"},
	}
	found := 0
	for _, b := range bodies {
		for _, p := range paths {
			v := lookup([]byte(b), p)
			if v.ok {
				found++
				if len(v.raw) == 0 {
					t.Errorf("lookup(%q, %q) found an empty value", b, p)
				}
			}
		}
	}
	if found < 20 {
		t.Fatalf("only %d lookups found a value; the check is not exercising lookup", found)
	}
}

// A raw value given as the JSON text `null` is valid JSON: it is written as null, as the old
// engine does (a Go nil, from a null in the config, is the one refused).
func TestRawNullText(t *testing.T) {
	for _, text := range []string{"null", " null "} {
		r := &Rules{
			OverrideRaw: []Rule{{Models: []Model{{Name: "*"}}, Params: map[string]any{"a": text}}},
			DefaultRaw:  []Rule{{Models: []Model{{Name: "*"}}, Params: map[string]any{"b": text}}},
		}
		if err := Validate(r); err != nil {
			t.Errorf("%q: Validate refused: %v", text, err)
		}
		got, _ := Apply(r, []byte(`{"a":1}`), Input{Model: "m"})
		if want := `{"a":` + text + `,"b":` + text + `}`; string(got) != want { // raw text is written as it is
			t.Errorf("%q: got %s, want %s", text, got, want)
		}
	}
}
