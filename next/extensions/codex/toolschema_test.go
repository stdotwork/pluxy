package main

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/extensions/codex/codextest"
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
		{`^(?!__.*__$)[a-z]+$`, true},
		{`foo(?=bar)`, true},
		{`a\\(?=b)`, true},      // escaped backslash, then unescaped lookahead
		{`[^]a](?=b)`, true},    // literal ] after [^ does not close class prematurely
		{`foo\(?!bar\)`, false}, // escaped paren is not a lookahead
		{`[(?=a)(?!b)]`, false}, // inside a character class
		{`[^](?=a)]`, false},    // still inside the character class
		{`(?<name>a)`, false},   // named capture group
		{`(?:a|b)`, false},      // non-capturing group
		{`(?i)a`, false},        // inline flag
		{`a\\p{L}`, false},      // an escaped backslash, then a literal p
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

// Only a property of the tool's parameters is converted from a const union to an enum: a root
// union is flattened rather than turned into an enum, and a deeper nested union is left alone.
func TestUnionToEnumOnlyForParameterProperties(t *testing.T) {
	schema := `{"type":"object","oneOf":` + consts(8) + `,"properties":{"o":{"type":"object","properties":{"deep":{"oneOf":` + consts(8) + `}}}}}`
	got, changed := fixed(t, schema)
	if !changed {
		t.Fatal("expected root union to be flattened")
	}
	sameJSON(t, got, `{"type":"object","properties":{"o":{"type":"object","properties":{"deep":{"oneOf":`+consts(8)+`}}}}}`)
}

func TestFixSchemaDropsLookaheadPatterns(t *testing.T) {
	got, changed := fixed(t, `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "pattern": "^(?!__.*__$)[a-z_]+$"},
			"ok": {"type": "string", "pattern": "^(?:foo|bar)$"},
			"fixed": {"default": {"pattern": "^(?!x)"}}
		},
		"patternProperties": {"^(?=x-)": {"type": "string"}, "^x-": {"type": "string", "pattern": "a(?=b)"}}
	}`)
	if !changed {
		t.Fatal("the schema was not changed")
	}
	sameJSON(t, got, `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"ok": {"type": "string", "pattern": "^(?:foo|bar)$"},
			"fixed": {"default": {"pattern": "^(?!x)"}}
		},
		"patternProperties": {"^x-": {"type": "string"}}
	}`)
}

func TestFixSchemaAddsMissingArrayItems(t *testing.T) {
	got, changed := fixed(t, `{
		"type": "object",
		"properties": {
			"bare": {"type": "array"},
			"nested": {"type": "array", "items": {"type": "array"}},
			"nullable": {"type": ["array", "null"]},
			"typed": {"type": "array", "items": {"type": "number"}},
			"refarr": {"type": "array", "$ref": "#/$defs/Arr"},
			"nonschema": {"default": {"type": "array"}}
		},
		"$defs": {"Arr": {"type": "array"}}
	}`)
	if !changed {
		t.Fatal("the schema was not changed")
	}
	sameJSON(t, got, `{
		"type": "object",
		"properties": {
			"bare": {"type": "array", "items": {"type": "string"}},
			"nested": {"type": "array", "items": {"type": "array", "items": {"type": "string"}}},
			"nullable": {"type": ["array", "null"], "items": {"type": "string"}},
			"typed": {"type": "array", "items": {"type": "number"}},
			"refarr": {"type": "array", "$ref": "#/$defs/Arr"},
			"nonschema": {"default": {"type": "array"}}
		},
		"$defs": {"Arr": {"type": "array", "items": {"type": "string"}}}
	}`)

	unchanged := `{"type":"object","properties":{"tags":{"type":"array","items":{"type":"string"}}}}`
	if out, changed := fixSchema(json.RawMessage(unchanged)); changed {
		t.Errorf("unchanged schema rewritten: %s", out)
	}
}

func TestFixSchemaRootUnion(t *testing.T) {
	t.Run("discriminated anyOf merges properties and intersects required", func(t *testing.T) {
		got, changed := fixed(t, `{
			"anyOf": [
				{
					"type": "object",
					"properties": {
						"status": {"type": "string", "enum": ["success"]},
						"shared": {"type": "string"},
						"value": {"type": "string"}
					},
					"required": ["status", "shared", "value"]
				},
				{
					"type": "object",
					"properties": {
						"status": {"type": "string", "enum": ["error"]},
						"shared": {"type": "string"},
						"error": {"type": "string"}
					},
					"required": ["status", "shared", "error"]
				}
			]
		}`)
		if !changed {
			t.Fatal("the schema was not changed")
		}
		sameJSON(t, got, `{
			"type": "object",
			"properties": {
				"status": {"anyOf": [{"type": "string", "enum": ["success"]}, {"type": "string", "enum": ["error"]}]},
				"shared": {"type": "string"},
				"value": {"type": "string"},
				"error": {"type": "string"}
			},
			"required": ["shared", "status"]
		}`)
	})

	t.Run("root oneOf with $ref and non-object fallback", func(t *testing.T) {
		got, changed := fixed(t, `{
			"oneOf": [
				{"$ref": "#/$defs/A"},
				{"type": "null"}
			],
			"$defs": {
				"A": {
					"type": "object",
					"properties": {"id": {"type": "string"}},
					"required": ["id"]
				}
			}
		}`)
		if !changed {
			t.Fatal("the schema was not changed")
		}
		sameJSON(t, got, `{
			"type": "object",
			"properties": {"id": {"type": "string"}},
			"required": ["id"],
			"$defs": {
				"A": {
					"type": "object",
					"properties": {"id": {"type": "string"}},
					"required": ["id"]
				}
			}
		}`)

		scalarUnion, changed := fixed(t, `{"anyOf":[{"type":"string"},{"type":"integer"}]}`)
		if !changed {
			t.Fatal("scalar root union was not changed")
		}
		sameJSON(t, scalarUnion, `{"type":"object","properties":{}}`)
	})
}

func TestFixSchemaStrictOptionalProperties(t *testing.T) {
	in := `{
		"type": "object",
		"properties": {
			"req": {"type": "string"},
			"optStr": {"type": "string"},
			"optEnum": {"type": "string", "enum": ["a", "b"]},
			"optUntypedEnum": {"enum": ["x", "y"]},
			"optAlreadyNull": {"type": ["integer", "null"]},
			"optAnyOf": {"anyOf": [{"type": "string"}, {"type": "number"}]},
			"optRef": {"$ref": "#/$defs/Child", "description": "optional child"},
			"optArr": {"type": "array"},
			"nested": {
				"type": "object",
				"properties": {
					"must": {"type": "boolean"},
					"may": {"type": "number"}
				},
				"required": ["must"]
			}
		},
		"required": ["req"],
		"$defs": {
			"Child": {
				"type": "object",
				"properties": {"a": {"type": "string"}, "b": {"type": "string"}},
				"required": ["a"]
			}
		}
	}`

	// Non-strict leaves optional properties untouched (except adding missing array items).
	nonStrict, changed := fixed(t, in)
	if !changed {
		t.Fatal("expected missing array items change in non-strict mode")
	}
	nonStrictMap := nonStrict.(map[string]any)
	if req := nonStrictMap["required"].([]any); len(req) != 1 || req[0] != "req" {
		t.Fatalf("non-strict changed required = %v, want [req]", req)
	}

	// Strict makes every optional property required and nullable recursively.
	out, changed, strictOff := fixSchemaForMode(json.RawMessage(in), true)
	if strictOff {
		t.Fatal("a closable schema turned strict off")
	}
	if !changed {
		t.Fatal("strict schema was not changed")
	}
	sameJSON(t, mustJSON(t, out), `{
		"type": "object",
		"properties": {
			"req": {"type": "string"},
			"optStr": {"type": ["string", "null"]},
			"optEnum": {"anyOf": [{"type": "string", "enum": ["a", "b"]}, {"type": "null"}]},
			"optUntypedEnum": {"enum": ["x", "y", null]},
			"optAlreadyNull": {"type": ["integer", "null"]},
			"optAnyOf": {"anyOf": [{"type": "string"}, {"type": "number"}, {"type": "null"}]},
			"optRef": {"description": "optional child", "anyOf": [{"$ref": "#/$defs/Child"}, {"type": "null"}]},
			"optArr": {"type": ["array", "null"], "items": {"type": "string"}},
			"nested": {
				"type": ["object", "null"],
				"properties": {
					"must": {"type": "boolean"},
					"may": {"type": ["number", "null"]}
				},
				"required": ["must", "may"],
				"additionalProperties": false
			}
		},
		"additionalProperties": false,
		"required": ["req", "nested", "optAlreadyNull", "optAnyOf", "optArr", "optEnum", "optRef", "optStr", "optUntypedEnum"],
		"$defs": {
			"Child": {
				"type": "object",
				"properties": {"a": {"type": "string"}, "b": {"type": ["string", "null"]}},
				"required": ["a", "b"],
				"additionalProperties": false
			}
		}
	}`)
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
	if got := reqs[0].body["parallel_tool_calls"]; got != false {
		t.Errorf("parallel_tool_calls upstream = %v, want false", got)
	}
}

// TestStrictToolSchemasThroughTheExtension runs the real extension process against a fake Codex
// upstream and checks that a tool schema combining all four CX7 forms (an array without items, a
// regex lookahead in pattern, an optional property under strict: true, and a root anyOf) arrives
// rewritten — both for a strict Responses passthrough request and for a translated Messages request.
func TestStrictToolSchemasThroughTheExtension(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	srv := codextest.New(t)
	acct := testAccount(srv, subscription(srv.IssueToken("text"), ""))
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)

	send := func(req llm.Request) codextest.Request {
		t.Helper()
		before := len(srv.Requests())
		s := ext.Generate(context.Background(), acct, req)
		defer s.Close()
		for {
			ev := s.Next()
			if ev.Type == llm.EventError {
				t.Fatalf("the call failed: %+v", ev.Error)
			}
			if ev.Terminal() {
				break
			}
		}
		all := srv.Requests()
		if len(all) != before+1 {
			t.Fatalf("%d requests reached the upstream after %d, want one more", len(all), before)
		}
		return all[before]
	}

	schema := `{
		"anyOf": [
			{
				"type": "object",
				"properties": {
					"mode": {"type": "string"},
					"query": {"type": "string", "pattern": "^(?!__)[a-z]+$"},
					"tags": {"type": "array"}
				},
				"required": ["mode", "query"]
			},
			{
				"type": "object",
				"properties": {
					"mode": {"type": "string"},
					"note": {"type": "string"}
				},
				"required": ["mode"]
			}
		]
	}`

	body := `{"model":"gpt-6.1-sol","input":"hi","tools":[{"type":"function","name":"search","strict":true,"parameters":` + schema + `}]}`
	r := send(responsesReq("gpt-6.1-sol", body, nil))
	rawTools, _ := json.Marshal(r.Body["tools"])
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil || len(tools) == 0 {
		t.Fatalf("upstream tools = %s, err = %v", rawTools, err)
	}
	sameJSON(t, mustJSON(t, tools[0]["parameters"]), `{
		"type": "object",
		"properties": {
			"mode": {"type": "string"},
			"query": {"type": ["string", "null"]},
			"tags": {"type": ["array", "null"], "items": {"type": "string"}},
			"note": {"type": ["string", "null"]}
		},
		"required": ["mode", "note", "query", "tags"],
		"additionalProperties": false
	}`)

	msgReq := llm.Request{
		Model: "gpt-6.1-sol",
		Items: []llm.Item{{Type: llm.ItemTypeMessage, Role: llm.RoleUser, Content: []llm.Part{{Type: llm.PartText, Text: "hi"}}}},
		Tools: []llm.Tool{{Name: "search", Parameters: json.RawMessage(schema)}},
		Native: &llm.Native{
			Format: llm.FormatAnthropicMessages,
			Body:   json.RawMessage(`{"model":"gpt-6.1-sol","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`),
		},
	}
	r = send(msgReq)
	rawTools, _ = json.Marshal(r.Body["tools"])
	if err := json.Unmarshal(rawTools, &tools); err != nil || len(tools) == 0 {
		t.Fatalf("translated upstream tools = %s, err = %v", rawTools, err)
	}
	sameJSON(t, mustJSON(t, tools[0]["parameters"]), `{
		"type": "object",
		"properties": {
			"mode": {"type": "string"},
			"query": {"type": "string"},
			"tags": {"type": "array", "items": {"type": "string"}},
			"note": {"type": "string"}
		},
		"required": ["mode"]
	}`)
}

// strictTools sends a Responses client's tools through buildRequest and returns the tools that
// would go upstream, with the translation's losses.
func strictTools(t *testing.T, tools string) ([]map[string]json.RawMessage, *llm.Translation) {
	t.Helper()
	body := `{"model":"gpt-test","input":[],"tools":` + tools + `}`
	ur, err := buildRequest(llm.Request{Model: "gpt-test", Native: &llm.Native{Format: llm.FormatOpenAIResponses, Body: json.RawMessage(body)}},
		"a", settings{ImageGenerationTool: new(bool)}, account{token: "k"}, false)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(ur.body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Tools, ur.tr
}

func strictFlag(t *testing.T, tool map[string]json.RawMessage) bool {
	t.Helper()
	var b bool
	if err := json.Unmarshal(tool["strict"], &b); err != nil {
		t.Fatalf("strict = %s: %v", tool["strict"], err)
	}
	return b
}

// everyObjectIsClosed reports each schema that has a properties key and no additionalProperties
// false, by walking the whole value (properties, $defs, items, anyOf and the rest alike).
func unclosedObjects(v any, path string, out *[]string) {
	switch s := v.(type) {
	case []any:
		for i, x := range s {
			unclosedObjects(x, path+"["+strconv.Itoa(i)+"]", out)
		}
	case map[string]any:
		if _, ok := s["properties"].(map[string]any); ok && s["additionalProperties"] != false {
			*out = append(*out, path)
		}
		for k, x := range s {
			switch k {
			case "enum", "const", "default", "examples", "required":
				continue
			}
			unclosedObjects(x, path+"."+k, out)
		}
	}
}

// Under strict: true every object schema that has properties is closed, wherever it sits.
func TestStrictClosesEveryObject(t *testing.T) {
	in := `{"type":"object","properties":{
		"nested":{"type":"object","properties":{"deep":{"type":"object","properties":{"x":{"type":"string"}}}}},
		"list":{"type":"array","items":{"type":"object","properties":{"y":{"type":"string"}}}},
		"either":{"anyOf":[{"type":"object","properties":{"z":{"type":"string"}}},{"type":"string"}]},
		"ref":{"$ref":"#/$defs/Child"},
		"empty":{"type":"object","properties":{}},
		"already":{"type":"object","properties":{"w":{"type":"string"}},"additionalProperties":false}
	},"$defs":{"Child":{"type":"object","properties":{"a":{"type":"string"}}}}}`
	tools, tr := strictTools(t, `[{"type":"function","name":"f","strict":true,"parameters":`+in+`}]`)
	if !strictFlag(t, tools[0]) {
		t.Fatalf("a schema that can be closed was sent non-strict: %s", tools[0]["parameters"])
	}
	var bad []string
	unclosedObjects(mustJSON(t, tools[0]["parameters"]), "$", &bad)
	if len(bad) > 0 {
		t.Errorf("objects left open: %v\n%s", bad, tools[0]["parameters"])
	}
	if len(tr.Losses) != 0 {
		t.Errorf("losses = %+v, want none", tr.Losses)
	}
}

// A schema that accepts more than its properties cannot be made strict without changing what the
// tool accepts: the tool goes out non-strict, with the non-strict rules' result, and one loss.
func TestStrictOpenSchemaGoesNonStrict(t *testing.T) {
	for name, params := range map[string]string{
		"a free-form map":                `{"type":"object","properties":{"meta":{"type":"object"},"tags":{"type":"array"}}}`,
		"a free-form root":               `{"type":"object"}`,
		"a nullable free map":            `{"type":"object","properties":{"m":{"type":["object","null"]}}}`,
		"a free map in $defs":            `{"type":"object","properties":{"c":{"$ref":"#/$defs/M"}},"$defs":{"M":{"type":"object"}}}`,
		"additionalProperties":           `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`,
		"an additionalProperties schema": `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":{"type":"string"}}`,
		"a nested open object":           `{"type":"object","properties":{"o":{"type":"object","properties":{"k":{"type":"string"}},"additionalProperties":{}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			tools, tr := strictTools(t, `[{"type":"function","name":"f","strict":true,"parameters":`+params+`}]`)
			if strictFlag(t, tools[0]) {
				t.Fatalf("sent strict: %s", tools[0]["parameters"])
			}
			// What the non-strict rules leave: nothing added to required, no nullability, and
			// the missing array items still filled in.
			want, _ := fixed(t, params)
			sameJSON(t, mustJSON(t, tools[0]["parameters"]), mustMarshal(t, want))
			if strings.Contains(string(tools[0]["parameters"]), `"required"`) {
				t.Errorf("strict rewrite applied: %s", tools[0]["parameters"])
			}
			if len(tr.Losses) != 1 {
				t.Fatalf("losses = %+v, want one", tr.Losses)
			}
			l := tr.Losses[0]
			if l.Kind != llm.LossApproximated || l.Path != "tools[0]" || !strings.Contains(l.Detail, "strict") {
				t.Errorf("loss = %+v", l)
			}
		})
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Inside a namespace tool, each function tool is judged on its own, and its loss names its path.
func TestStrictNamespaceTools(t *testing.T) {
	closed := `{"type":"object","properties":{"a":{"type":"string"}}}`
	open := `{"type":"object","properties":{"a":{"type":"string"},"m":{"type":"object"}}}`
	tools, tr := strictTools(t, `[{"type":"namespace","name":"mcp__s","description":"d","tools":[
		{"type":"function","name":"ok","strict":true,"parameters":`+closed+`},
		{"type":"function","name":"open","strict":true,"parameters":`+open+`}]}]`)
	var nested []map[string]json.RawMessage
	if err := json.Unmarshal(tools[0]["tools"], &nested); err != nil {
		t.Fatal(err)
	}
	if !strictFlag(t, nested[0]) || strictFlag(t, nested[1]) {
		t.Fatalf("strict flags = %v, %v, want true, false", strictFlag(t, nested[0]), strictFlag(t, nested[1]))
	}
	sameJSON(t, mustJSON(t, nested[0]["parameters"]), `{"type":"object","properties":{"a":{"type":["string","null"]}},"required":["a"],"additionalProperties":false}`)
	sameJSON(t, mustJSON(t, nested[1]["parameters"]), open)
	if len(tr.Losses) != 1 || tr.Losses[0].Path != "tools[0].tools[1]" || tr.Losses[0].Kind != llm.LossApproximated {
		t.Errorf("losses = %+v, want one at tools[0].tools[1]", tr.Losses)
	}
}

// A tool that is not strict goes out as the rules for any tool leave it: no closing, no
// required, no nullability, no strict flag turned on or off, and no loss.
func TestNonStrictToolsAreNotClosed(t *testing.T) {
	params := `{"type":"object","properties":{"a":{"type":"string"},"m":{"type":"object"},"o":{"type":"object","properties":{"k":{"type":"string"}}}}}`
	for name, tool := range map[string]string{
		"strict false": `{"type":"function","name":"f","strict":false,"parameters":` + params + `}`,
		"no strict":    `{"type":"function","name":"f","parameters":` + params + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			tools, tr := strictTools(t, `[`+tool+`]`)
			if got := string(tools[0]["parameters"]); got != params {
				t.Errorf("parameters changed:\n got %s\nwant %s", got, params)
			}
			if s, ok := tools[0]["strict"]; ok && string(s) != "false" {
				t.Errorf("strict = %s", s)
			}
			if len(tr.Losses) != 0 {
				t.Errorf("losses = %+v", tr.Losses)
			}
		})
	}
}

// acceptsNull reads a schema's shape, not its words: null passes when every one of type, enum,
// const, anyOf and oneOf that the schema has lets it through.
func acceptsNull(s map[string]any) bool {
	seen := false
	if v, ok := s["type"]; ok {
		seen = true
		found := v == "null"
		if l, ok := v.([]any); ok {
			for _, x := range l {
				found = found || x == "null"
			}
		}
		if !found {
			return false
		}
	}
	if v, ok := s["enum"].([]any); ok {
		seen = true
		found := false
		for _, x := range v {
			found = found || x == nil
		}
		if !found {
			return false
		}
	}
	if v, ok := s["const"]; ok {
		seen = true
		if v != nil {
			return false
		}
	}
	for _, k := range []string{"anyOf", "oneOf"} {
		if l, ok := s[k].([]any); ok {
			seen = true
			found := false
			for _, b := range l {
				if bm, ok := b.(map[string]any); ok && acceptsNull(bm) {
					found = true
				}
			}
			if !found {
				return false
			}
		}
	}
	return seen
}

// An optional property is made required, so null must pass every keyword that constrains it.
func TestStrictOptionalPropertyAcceptsNull(t *testing.T) {
	props := map[string]string{
		"plainType":       `{"type":"string"}`,
		"typeList":        `{"type":["string","integer"]}`,
		"plainAnyOf":      `{"anyOf":[{"type":"string"},{"type":"number"}]}`,
		"plainOneOf":      `{"oneOf":[{"type":"string"},{"type":"number"}]}`,
		"plainEnum":       `{"enum":["x","y"]}`,
		"typeEnum":        `{"type":"string","enum":["a","b"],"description":"d","title":"T"}`,
		"typeAnyOf":       `{"type":"string","anyOf":[{"minLength":1},{"maxLength":0}],"description":"d"}`,
		"typeOneOf":       `{"type":"string","oneOf":[{"minLength":1},{"maxLength":0}]}`,
		"anyOfAndOneOf":   `{"anyOf":[{"type":"string"},{"type":"number"}],"oneOf":[{"minimum":1},{"maximum":0}]}`,
		"enumAndAnyOf":    `{"enum":["a","b"],"anyOf":[{"const":"a"},{"const":"b"}]}`,
		"typeNullInList":  `{"type":["string","null"],"enum":["a","b"]}`,
		"nullBranchOnly":  `{"type":"string","anyOf":[{"type":"string"},{"type":"null"}]}`,
		"alreadyNullable": `{"type":["integer","null"]}`,
	}
	var parts []string
	for k, v := range props {
		parts = append(parts, `"`+k+`":`+v)
	}
	params := `{"type":"object","properties":{` + strings.Join(parts, ",") + `}}`
	tools, _ := strictTools(t, `[{"type":"function","name":"f","strict":true,"parameters":`+params+`}]`)
	out := mustJSON(t, tools[0]["parameters"]).(map[string]any)
	got := out["properties"].(map[string]any)
	for name := range props {
		pm, ok := got[name].(map[string]any)
		if !ok || !acceptsNull(pm) {
			t.Errorf("%s refuses null: %s", name, mustMarshal(t, got[name]))
		}
	}
	// The forms that already took a null keep their shape; the rest are wrapped, with the
	// description and title outside.
	sameJSON(t, got["plainType"], `{"type":["string","null"]}`)
	sameJSON(t, got["typeList"], `{"type":["string","integer","null"]}`)
	sameJSON(t, got["plainAnyOf"], `{"anyOf":[{"type":"string"},{"type":"number"},{"type":"null"}]}`)
	sameJSON(t, got["plainOneOf"], `{"oneOf":[{"type":"string"},{"type":"number"},{"type":"null"}]}`)
	sameJSON(t, got["plainEnum"], `{"enum":["x","y",null]}`)
	sameJSON(t, got["typeEnum"], `{"description":"d","title":"T","anyOf":[{"type":"string","enum":["a","b"]},{"type":"null"}]}`)
	sameJSON(t, got["typeAnyOf"], `{"description":"d","anyOf":[{"type":"string","anyOf":[{"minLength":1},{"maxLength":0}]},{"type":"null"}]}`)
	sameJSON(t, got["alreadyNullable"], `{"type":["integer","null"]}`)
}
