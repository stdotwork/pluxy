package main

import (
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func TestRejectedPattern(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		want    bool
	}{
		{`^\p{L}+$`, true},
		{`\P{Lu}`, true},
		{`^[^\0]*$`, true},
		{`a\\p{L}`, false}, // an escaped backslash, then a literal p
		{`a\\0`, false},
		{`^\d+\p`, false}, // \p with no brace is not a property escape
		{`^[a-z]+\.\w{2,3}$`, false},
		{`é`, false},
		{`trailing\`, false},
		{``, false},
	} {
		if got := rejectedPattern(tc.pattern); got != tc.want {
			t.Errorf("rejectedPattern(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// fixed runs fixSchema on schema and returns the result as a JSON value, and whether it changed.
func fixed(t *testing.T, schema string) (any, bool) {
	t.Helper()
	out, changed := fixSchema(json.RawMessage(schema))
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("the result is not JSON: %v: %s", err, out)
	}
	return v, changed
}

func sameJSON(t *testing.T, got any, want string) {
	t.Helper()
	var w any
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, w) {
		g, _ := json.Marshal(got)
		t.Errorf("got  %s\nwant %s", g, want)
	}
}

// A rejected pattern goes wherever a schema can sit, a patternProperties key goes with its
// schema, and what is not a schema (an enum, a const, a default) is not read as one.
func TestFixSchemaDropsRejectedPatterns(t *testing.T) {
	got, changed := fixed(t, `{
		"type": "object",
		"pattern": "^\\p{L}+$",
		"properties": {
			"name": {"type": "string", "pattern": "^\\P{Lu}"},
			"pattern": {"type": "string", "pattern": "^ok$"},
			"list": {"type": "array", "items": {"type": "string", "pattern": "^[^\\0]*$"}},
			"either": {"anyOf": [{"type": "string", "pattern": "\\p{N}"}, {"type": "null"}]},
			"fixed": {"const": {"pattern": "\\p{L}"}, "default": {"pattern": "\\p{L}"}, "enum": [{"pattern": "\\p{L}"}]}
		},
		"patternProperties": {"^\\p{L}+$": {"type": "string"}, "^x-": {"type": "string", "pattern": "\\p{L}"}},
		"$defs": {"id": {"type": "string", "pattern": "\\P{L}"}}
	}`)
	if !changed {
		t.Fatal("the schema was not changed")
	}
	sameJSON(t, got, `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"pattern": {"type": "string", "pattern": "^ok$"},
			"list": {"type": "array", "items": {"type": "string"}},
			"either": {"anyOf": [{"type": "string"}, {"type": "null"}]},
			"fixed": {"const": {"pattern": "\\p{L}"}, "default": {"pattern": "\\p{L}"}, "enum": [{"pattern": "\\p{L}"}]}
		},
		"patternProperties": {"^x-": {"type": "string"}},
		"$defs": {"id": {"type": "string"}}
	}`)
}

// A bound goes only when it is a whole number outside the int64 range; the digits of every other
// bound stay as the client wrote them.
func TestFixSchemaDropsBoundsOutsideInt64(t *testing.T) {
	for _, tc := range []struct {
		bound string
		drop  bool
	}{
		{"9223372036854775807", false},
		{"-9223372036854775808", false},
		{"9223372036854775808", true},
		{"-9223372036854775809", true},
		{"18446744073709551615", true},
		{"1e19", true},
		{"1E+30", true},
		{"9.3e18", true},
		{"-1e400", true},
		{"1e99999999999999999999", true},
		{"922337203685477580.7e1", false}, // 9223372036854775807
		{"1e18", false},
		{"1e3", false},
		{"0.5e1", false},
		{"1.5", false},
		{"123456789012345678901234567890.5", false}, // not a whole number
		{"1e-400", false},
		{"0", false},
		{"0e99999999999999999999", false},
		{"-0.0", false},
	} {
		for _, kw := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
			schema := `{"type":"object","properties":{"n":{"type":"number","` + kw + `":` + tc.bound + `,"description":"d"}}}`
			out, changed := fixSchema(json.RawMessage(schema))
			if changed != tc.drop {
				t.Errorf("%s %s: changed = %v, want %v", kw, tc.bound, changed, tc.drop)
			}
			if tc.drop {
				sameJSON(t, mustJSON(t, out), `{"type":"object","properties":{"n":{"type":"number","description":"d"}}}`)
			} else if string(out) != schema {
				t.Errorf("%s %s: an unchanged schema was rewritten: %s", kw, tc.bound, out)
			}
		}
	}
}

func mustJSON(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// A bound that is not a number (a malformed schema) is not the rule's business.
func TestFixSchemaLeavesOtherShapesAlone(t *testing.T) {
	for _, schema := range []string{
		`{"type":"object","properties":{"n":{"minimum":"1e400"}}}`,
		`{"type":"object","properties":"nope"}`,
		`["\\p{L}"]`,
		`"x"`,
		`null`,
		``,
		`{broken`,
	} {
		if out, changed := fixSchema(json.RawMessage(schema)); changed || string(out) != schema {
			t.Errorf("fixSchema(%q) = %q, %v; want it left alone", schema, out, changed)
		}
	}
}

// consts builds n const branches "v0", "v1", ... each with a description.
func consts(n int) string {
	var b []string
	for i := 0; i < n; i++ {
		b = append(b, `{"const":"v`+string(rune('a'+i))+`","description":"d"}`)
	}
	return "[" + strings.Join(b, ",") + "]"
}

func TestUnionToEnum(t *testing.T) {
	enum8 := `["va","vb","vc","vd","ve","vf","vg","vh"]`
	for _, tc := range []struct {
		name string
		prop string
		want string // the property after, or "" for unchanged
	}{
		{"oneOf becomes an enum", `{"type":"string","description":"p","oneOf":` + consts(8) + `}`,
			`{"type":"string","description":"p","enum":` + enum8 + `}`},
		{"anyOf becomes an enum", `{"anyOf":` + consts(8) + `}`, `{"enum":` + enum8 + `}`},
		{"below the threshold", `{"oneOf":` + consts(7) + `}`, ""},
		{"title and description are allowed, nothing else", `{"oneOf":[{"const":"a"},{"const":"b","title":"t"},{"const":"c","description":"d"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}`,
			`{"enum":["a","b","c","d","e","f","g","h"]}`},
		{"a branch with a type", `{"oneOf":[{"const":"a","type":"string"},{"const":"b"},{"const":"c"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}`, ""},
		{"a branch without a const", `{"oneOf":[{"type":"null"},{"const":"b"},{"const":"c"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}`, ""},
		{"a branch that is not an object", `{"oneOf":["a",{"const":"b"},{"const":"c"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}`, ""},
		{"an object const", `{"oneOf":[{"const":{"a":1}},{"const":"b"},{"const":"c"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}`, ""},
		{"a repeated value", `{"oneOf":[{"const":"a"},{"const":"a","description":"again"},{"const":"c"},{"const":"d"},{"const":"e"},{"const":"f"},{"const":"g"},{"const":"h"}]}`, ""},
		{"1 and 1.0 are one value", `{"oneOf":[{"const":1},{"const":1.0},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`, ""},
		{"1e2 and 100 are one value", `{"oneOf":[{"const":100},{"const":1e2},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`, ""},
		{"\"1\" and 1 are different values", `{"oneOf":[{"const":"1"},{"const":1},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`,
			`{"enum":["1",1,3,4,5,6,7,8]}`},
		{"huge exponents that differ are different values", `{"oneOf":[{"const":1e99999999999999999999},{"const":1e99999999999999999998},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`,
			`{"enum":[1e99999999999999999999,1e99999999999999999998,3,4,5,6,7,8]}`},
		{"huge exponents that are equal are one value", `{"oneOf":[{"const":1e99999999999999999999},{"const":10e99999999999999999998},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`, ""},
		{"0 and -0.0 are one value", `{"anyOf":[{"const":0},{"const":-0.0},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`, ""},
		{"both oneOf and anyOf", `{"oneOf":` + consts(8) + `,"anyOf":` + consts(8) + `}`, ""},
		{"not a list", `{"oneOf":"nope"}`, ""},
		{"an equal enum makes the union redundant", `{"enum":["vh","vg","vf","ve","vd","vc","vb","va"],"oneOf":` + consts(8) + `}`,
			`{"enum":["vh","vg","vf","ve","vd","vc","vb","va"]}`},
		{"an equal enum of numbers", `{"enum":[1,2,3,4,5,6,7,8.0],"oneOf":[{"const":1.0},{"const":2},{"const":3},{"const":4},{"const":5},{"const":6},{"const":7},{"const":8}]}`,
			`{"enum":[1,2,3,4,5,6,7,8.0]}`},
		{"a different enum", `{"enum":["va","vb","vc","vd","ve","vf","vg","zz"],"oneOf":` + consts(8) + `}`, ""},
		{"a shorter enum", `{"enum":["va"],"oneOf":` + consts(8) + `}`, ""},
		{"an enum that repeats a value", `{"enum":["va","va","vc","vd","ve","vf","vg","vh"],"oneOf":` + consts(8) + `}`, ""},
		{"an enum that is not a list", `{"enum":"va","oneOf":` + consts(8) + `}`, ""},
		{"an enum with an object", `{"enum":["va","vb","vc","vd","ve","vf","vg",{}],"oneOf":` + consts(8) + `}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var prop map[string]any
			dec := json.NewDecoder(strings.NewReader(tc.prop))
			dec.UseNumber()
			if err := dec.Decode(&prop); err != nil {
				t.Fatal(err)
			}
			changed := unionToEnum(prop)
			if changed != (tc.want != "") {
				t.Fatalf("changed = %v, want %v", changed, tc.want != "")
			}
			if !changed {
				return
			}
			got, _ := json.Marshal(prop)
			var g, w any
			_ = json.Unmarshal(got, &g)
			_ = json.Unmarshal([]byte(tc.want), &w)
			if !reflect.DeepEqual(g, w) {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

// The enum keeps the digits of a number const, and the order of the branches.
func TestUnionToEnumKeepsConstDigits(t *testing.T) {
	out, changed := fixSchema(json.RawMessage(`{"type":"object","properties":{"n":{"oneOf":[
		{"const":12345678901234567890123},{"const":1.50},{"const":"x"},{"const":true},
		{"const":false},{"const":null},{"const":0},{"const":-2}]}}}`))
	if !changed {
		t.Fatal("the schema was not changed")
	}
	if want := `"enum":[12345678901234567890123,1.50,"x",true,false,null,0,-2]`; !strings.Contains(string(out), want) {
		t.Errorf("%s does not contain %s", out, want)
	}
}

// Only a property of the tool's parameters is read for a union, as today's core does: one deeper
// down, or the schema itself, is left as written.
func TestUnionToEnumOnlyForParameterProperties(t *testing.T) {
	schema := `{"type":"object","oneOf":` + consts(8) + `,"properties":{"o":{"type":"object","properties":{"deep":{"oneOf":` + consts(8) + `}}}}}`
	if out, changed := fixSchema(json.RawMessage(schema)); changed {
		t.Errorf("changed: %s", out)
	}
}

// The rules apply to every function tool: a Responses client's own, one inside a namespace tool,
// and one translated from another format. Tools of other types, and tools with nothing to fix,
// go out as they came.
func TestBuildRequestFixesToolSchemas(t *testing.T) {
	bad := `{"type":"object","properties":{"id":{"type":"string","pattern":"^\\p{L}+$"},"n":{"type":"integer","maximum":1e30},` +
		`"mode":{"oneOf":` + consts(8) + `}}}`
	good := `{"type":"object","properties":{"id":{"type":"string","pattern":"^[a-z]+$"}}}`
	wantBad := `{"type":"object","properties":{"id":{"type":"string"},"n":{"type":"integer"},"mode":{"enum":["va","vb","vc","vd","ve","vf","vg","vh"]}}}`

	tools := func(t *testing.T, req llm.Request) []map[string]json.RawMessage {
		t.Helper()
		ur, err := buildRequest(req, "a", settings{ImageGenerationTool: new(bool)}, account{token: "k"}, false)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(ur.body, &body); err != nil {
			t.Fatal(err)
		}
		return body.Tools
	}
	params := func(t *testing.T, tool map[string]json.RawMessage) any {
		t.Helper()
		return mustJSON(t, tool["parameters"])
	}

	t.Run("a Responses client's tools", func(t *testing.T) {
		body := `{"model":"gpt-test","input":[],"tools":[
			{"type":"function","name":"bad","parameters":` + bad + `},
			{"type":"function","name":"good","parameters":` + good + `},
			{"type":"namespace","name":"mcp__srv","description":"s","tools":[
				{"type":"function","name":"nested","parameters":` + bad + `},
				{"type":"function","name":"nested_good","parameters":` + good + `}]},
			{"type":"web_search"}]}`
		got := tools(t, llm.Request{Model: "gpt-test", Native: &llm.Native{Format: llm.FormatOpenAIResponses, Body: json.RawMessage(body)}})
		if len(got) != 4 {
			t.Fatalf("%d tools, want 4", len(got))
		}
		sameJSON(t, params(t, got[0]), wantBad)
		sameJSON(t, params(t, got[1]), good)
		var nested []map[string]json.RawMessage
		if err := json.Unmarshal(got[2]["tools"], &nested); err != nil {
			t.Fatal(err)
		}
		sameJSON(t, params(t, nested[0]), wantBad)
		sameJSON(t, params(t, nested[1]), good)
		sameJSON(t, mustJSON(t, got[2]["description"]), `"s"`)
		sameJSON(t, mustJSON(t, got[3]["type"]), `"web_search"`)
		if _, ok := got[3]["parameters"]; ok {
			t.Errorf("web_search gained parameters: %s", got[3]["parameters"])
		}
	})

	t.Run("a translated client's tools", func(t *testing.T) {
		for _, format := range []string{llm.FormatAnthropicMessages, llm.FormatOpenAIChat, llm.FormatGeminiGenerate} {
			req := llm.Request{Model: "gpt-test", Native: &llm.Native{Format: format, Body: json.RawMessage(`{}`)}, Tools: []llm.Tool{
				{Name: "bad", Parameters: json.RawMessage(bad)},
				{Name: "good", Parameters: json.RawMessage(good)},
			}}
			got := tools(t, req)
			if len(got) != 2 {
				t.Fatalf("%s: %d tools, want 2", format, len(got))
			}
			p := params(t, got[0]).(map[string]any)
			if format == llm.FormatGeminiGenerate {
				delete(p, "additionalProperties") // closedSchema adds it at the top
			}
			sameJSON(t, p, wantBad)
		}
	})
}

// A compaction request's tools go upstream with the same schema rewrites as on /responses, as
// today's core sends them.
func TestCompactFixesToolSchemas(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	u := newSearchUpstream(t)
	acct := searchAccount(t, u, "/v1", &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-compact"}, nil)
	body := `{"model":"m","input":"hi","tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{"s":{"type":"string","pattern":"^\\p{L}+$"}}}}]}`
	s := ext.Endpoint(t.Context(), acct, llm.Request{Model: "m", Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(body)}})
	for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
	}
	s.Close()
	reqs := u.requests()
	if len(reqs) != 1 || reqs[0].path != "/v1/responses/compact" {
		t.Fatalf("upstream requests = %+v", reqs)
	}
	got, _ := json.Marshal(reqs[0].body["tools"])
	if want := `[{"name":"f","parameters":{"properties":{"s":{"type":"string"}},"type":"object"},"type":"function"}]`; string(got) != want {
		t.Errorf("tools upstream = %s, want %s", got, want)
	}
}

// A compaction request whose client_metadata says it is Responses-lite has its replayed web
// searches removed, as on /responses.
func TestCompactLiteFromClientMetadata(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	u := newSearchUpstream(t)
	acct := searchAccount(t, u, "/v1", &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "sk-compact"}, nil)
	body := `{"model":"m","input":[` + userMsg + `,` + webSearchItem + `],"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"}}`
	s := ext.Endpoint(t.Context(), acct, llm.Request{Model: "m", Native: &llm.Native{Format: llm.FormatOpenAIResponsesCompact, Body: json.RawMessage(body)}})
	for ev := s.Next(); !ev.Terminal(); ev = s.Next() {
	}
	s.Close()
	reqs := u.requests()
	if len(reqs) != 1 {
		t.Fatalf("upstream requests = %+v", reqs)
	}
	got, _ := json.Marshal(reqs[0].body["input"])
	if strings.Contains(string(got), "web_search_call") || reqs[0].body["tools"] != nil {
		t.Errorf("input upstream = %s, tools %v; want the replayed search removed and no tool", got, reqs[0].body["tools"])
	}
}
