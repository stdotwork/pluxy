package payload

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
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

// Select, sent as JSON (the call frame's form), and Apply with Selected and nothing else about
// the call give what the old engine returned: core matches the call, the extension only the body.
func TestSelectThenApplyMatchesOldEngine(t *testing.T) {
	selected := 0
	for _, c := range loadCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			in := Input{
				Model: c.Input.Model, RequestedModel: c.Input.RequestedModel, Protocol: c.Input.Protocol,
				FromProtocol: c.Input.FromProtocol, Headers: http.Header{},
			}
			for k, vs := range c.Input.Headers {
				for _, v := range vs {
					in.Headers.Add(k, v)
				}
			}
			sel := Select(&c.Rules, in)
			var wire *Rules
			if sel != nil {
				selected++
				b, err := json.Marshal(sel)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(b, &wire); err != nil {
					t.Fatal(err)
				}
			}
			seam := Input{Root: c.Input.Root, Selected: true}
			if c.Input.Original != nil {
				seam.Original = []byte(*c.Input.Original)
			}
			if got, _ := Apply(wire, []byte(c.Body), seam); string(got) != c.Old {
				t.Errorf("body\n got: %s\nwant: %s", got, c.Old)
			}
		})
	}
	if selected < 100 {
		t.Fatalf("only %d cases selected a rule", selected)
	}
}

// Select keeps only the models that match the call, without what core alone can check.
func TestSelectKeepsBodyConditionsOnly(t *testing.T) {
	r := &Rules{Override: []Rule{
		{Models: []Model{{Name: "gpt-*", Protocol: "codex", Headers: map[string]string{"X-Tier": "gold"}, Exist: []string{"tools"}},
			{Name: "gpt-*", Protocol: "claude"}}, Params: map[string]any{"a": 1}},
		{Models: []Model{{Name: "other"}}, Params: map[string]any{"b": 2}},
	}}
	h := http.Header{}
	h.Set("x-tier", "gold")
	got, _ := json.Marshal(Select(r, Input{Model: "gpt-5", Protocol: "codex", Headers: h}))
	if want := `{"override":[{"models":[{"exist":["tools"]}],"params":{"a":1}}]}`; string(got) != want {
		t.Errorf("Select = %s, want %s", got, want)
	}
	if s := Select(r, Input{Model: "gpt-5", Protocol: "codex"}); s != nil {
		t.Errorf("without the header Select = %+v, want nil", s)
	}
	if s := Select(r, Input{Protocol: "codex", Headers: h}); s != nil {
		t.Errorf("without a model Select = %+v, want nil", s)
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
