package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// toolsBody wraps parametersJsonSchema schemas as the functionDeclarations of one tool.
func toolsBody(t *testing.T, schemas ...string) []byte {
	t.Helper()
	var decls []string
	for i, s := range schemas {
		decls = append(decls, fmt.Sprintf(`{"name":"tool%d","parametersJsonSchema":%s}`, i, s))
	}
	return []byte(`{"contents":[],"tools":[{"functionDeclarations":[` + strings.Join(decls, ",") + `]}]}`)
}

// cleanedSchemas runs the request path over body and returns each declaration's schema as text.
func cleanedSchemas(t *testing.T, body []byte) []string {
	t.Helper()
	out := cleanToolSchemas(body)
	var sent struct {
		Tools []struct {
			FunctionDeclarations []struct {
				ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(out, &sent); err != nil {
		t.Fatalf("cleaned body is not JSON: %v", err)
	}
	var schemas []string
	for _, d := range sent.Tools[0].FunctionDeclarations {
		schemas = append(schemas, string(d.ParametersJSONSchema))
	}
	return schemas
}

// fanoutSchema is a schema whose def d<i> holds four $refs to d<i+1>: expanding it in full
// multiplies its size by four at every level. The last def is a string.
func fanoutSchema(levels int) string {
	var defs []string
	for i := 0; i < levels; i++ {
		body := `{"type":"string"}`
		if i < levels-1 {
			var props []string
			for p := 0; p < 4; p++ {
				props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#/$defs/d%d"}`, p, i+1))
			}
			body = `{"type":"object","description":"level ` + fmt.Sprint(i) + `","properties":{` + strings.Join(props, ",") + `}}`
		}
		defs = append(defs, fmt.Sprintf(`"d%d":%s`, i, body))
	}
	return `{"type":"object","$defs":{` + strings.Join(defs, ",") + `},"properties":{"x":{"$ref":"#/$defs/d0"}}}`
}

// TestRefFanoutIsBounded: ten levels of four refs is a schema of about 1.5 KB that, expanded in
// full, is tens of megabytes (every level adds a factor of four; the seventeenth would need
// about 150 GB). The request is still sent, with the refs the budget could not pay for replaced by
// a "See:" hint.
func TestRefFanoutIsBounded(t *testing.T) {
	in := fanoutSchema(10)
	if len(in) > 4000 {
		t.Fatalf("test schema is %d bytes, want a small one", len(in))
	}
	body := toolsBody(t, in)
	schemas := cleanedSchemas(t, body)
	got := schemas[0]
	if len(got) > 4<<20 {
		t.Fatalf("a %d byte schema became %d bytes", len(in), len(got))
	}
	if strings.Contains(got, `$ref`) {
		t.Errorf("a $ref survived")
	}
	if !strings.Contains(got, "See: d") {
		t.Errorf("no ref took the \"See:\" fallback")
	}
	if !strings.Contains(got, `"x":{"description":"level 0","properties":`) {
		t.Errorf("the first levels are not expanded: %.300s", got)
	}
	for range 3 {
		if again := cleanedSchemas(t, body)[0]; again != got {
			t.Fatal("output varies between runs")
		}
	}
}

// TestRefBudgetIsSharedByAllTools: the budget is per request, not per tool, or many tools would
// multiply it. Every tool here inlines one def of 500 properties twice.
func TestRefBudgetIsSharedByAllTools(t *testing.T) {
	var props []string
	for i := 0; i < 500; i++ {
		props = append(props, fmt.Sprintf(`"f%d":{"type":"string"}`, i))
	}
	schema := `{"type":"object","$defs":{"big":{"type":"object","description":"the big def","properties":{` +
		strings.Join(props, ",") + `}}},"properties":{"a":{"$ref":"#/$defs/big"},"b":{"$ref":"#/$defs/big"}}}`
	const tools = 100
	schemas := make([]string, tools)
	for i := range schemas {
		schemas[i] = schema
	}
	got := cleanedSchemas(t, toolsBody(t, schemas...))
	if len(got) != tools {
		t.Fatalf("%d tools came back, want %d", len(got), tools)
	}
	if strings.Contains(got[0], "See:") || !strings.Contains(got[0], `"f499"`) {
		t.Errorf("the first tool is not fully inlined: %.200s", got[0])
	}
	last := got[tools-1]
	var m struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(last), &m); err != nil {
		t.Fatal(err)
	}
	a := m.Properties["a"]
	aProps, _ := a["properties"].(map[string]any)
	if a["type"] != "object" || a["description"] != "the big def (See: big)" || a["properties"] != nil {
		t.Errorf("the last tool's refused ref has type %v, description %q and %v properties, want the target's type, its description with a hint, and none", a["type"], a["description"], len(aProps))
	}
	total := 0
	for _, s := range got {
		total += len(s)
	}
	if one := len(got[0]); total > 40*one {
		t.Errorf("%d tools came to %d bytes, %d times the first: the budget is not shared", tools, total, total/one)
	}
}

// TestRefReusedDefIsInlined: an ordinary schema that uses one small def many times is expanded
// in full.
func TestRefReusedDefIsInlined(t *testing.T) {
	var props []string
	for i := 0; i < 30; i++ {
		props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#/$defs/point"}`, i))
	}
	schema := `{"type":"object","$defs":{"point":{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"}},"required":["x","y"]}},"properties":{` +
		strings.Join(props, ",") + `}}`
	got := cleanedSchemas(t, toolsBody(t, schema))[0]
	if strings.Contains(got, "See:") || strings.Contains(got, "$ref") {
		t.Errorf("a reused def was not inlined: %s", got)
	}
	if n := strings.Count(got, `"required":["x","y"]`); n != 30 {
		t.Errorf("%d of 30 uses were inlined", n)
	}
}

// TestRefLongDescriptionIsBounded: the budget counts bytes, not only values. One def with a long
// description and a thousand refs to it is a small request whose full expansion is a thousand
// copies of the description.
func TestRefLongDescriptionIsBounded(t *testing.T) {
	var props []string
	for i := 0; i < 1000; i++ {
		props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#/$defs/long"}`, i))
	}
	schema := `{"type":"object","$defs":{"long":{"type":"object","description":"` + strings.Repeat("d", 50000) +
		`"}},"properties":{` + strings.Join(props, ",") + `}}`
	got := cleanedSchemas(t, toolsBody(t, schema))[0]
	if len(got) > 6<<20 {
		t.Errorf("a %d byte schema became %d bytes", len(schema), len(got))
	}
	if strings.Contains(got, "$ref") || !strings.Contains(got, "See: long") {
		t.Errorf("the refs were not all resolved, or none was refused")
	}
}

// TestRefRefusedTargetsLongTypeIsNotCloned: a refused ref keeps its target's type only when it
// is short. A target with a type list of 60,000 strings and 200 refs to it must not clone the
// list 200 times.
func TestRefRefusedTargetsLongTypeIsNotCloned(t *testing.T) {
	var props []string
	for i := 0; i < 200; i++ {
		props = append(props, fmt.Sprintf(`"p%d":{"$ref":"#/$defs/wide"}`, i))
	}
	schema := `{"type":"object","$defs":{"wide":{"type":[` + strings.TrimSuffix(strings.Repeat(`"string",`, 60000), ",") +
		`]}},"properties":{` + strings.Join(props, ",") + `}}`
	body := toolsBody(t, schema)

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := cleanedSchemas(t, body)[0]
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Errorf("cleaning a %d byte schema allocated %d MiB", len(schema), alloc>>20)
	}
	if strings.Contains(got, "$ref") {
		t.Errorf("a $ref survived")
	}
}

// Once the budget is spent, a refused ref keeps nothing of its target, so many refs to a target
// with a long type list cost a small fixed stub each.
func TestRefusedRefKeepsNothingOnceBudgetIsSpent(t *testing.T) {
	in := `{"type":"object","$defs":{"d":{"type":["string","number","integer","boolean","object","array","null"],"nullable":true,"description":"kept only if paid for"}},"properties":{"p":{"$ref":"#/$defs/d"}}}`
	out, _ := cleanToolSchema(json.RawMessage(in), false, &refBudget{})
	var got struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "object", "description": "See: d"}
	if !reflect.DeepEqual(got.Properties["p"], want) {
		t.Errorf("p = %#v, want %#v", got.Properties["p"], want)
	}
}

// TestRefFallbackKeepsTargetFieldsForCyclesAndDepthCap checks that cyclic and depth-capped $ref
// stubs look up their target and keep its short type, nullable, and description (when the budget
// can pay for them), while letting sibling fields on the $ref node override the target.
func TestRefFallbackKeepsTargetFieldsForCyclesAndDepthCap(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		in := `{
			"$defs": {
				"list": {
					"type": "array",
					"nullable": true,
					"description": "Nested list",
					"items": {"$ref": "#/$defs/list"}
				}
			},
			"type": "object",
			"properties": {
				"root": {"$ref": "#/$defs/list"}
			}
		}`
		out, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
		var got struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		items, _ := got.Properties["root"]["items"].(map[string]any)
		if !reflect.DeepEqual(items["type"], []any{"array", "null"}) {
			t.Errorf("cyclic items type = %#v, want [array null]", items["type"])
		}
		if items["description"] != "Nested list (See: list)" {
			t.Errorf("cyclic items description = %q, want %q", items["description"], "Nested list (See: list)")
		}
		if sub, ok := items["items"].(map[string]any); !ok || sub["type"] != "string" {
			t.Errorf("cyclic items.items = %#v, want {type:string}", items["items"])
		}
	})

	t.Run("cycle sibling overrides target description", func(t *testing.T) {
		in := `{
			"$defs": {
				"node": {
					"type": "object",
					"description": "Tree node",
					"properties": {
						"child": {"$ref": "#/$defs/node", "description": "Child node"}
					}
				}
			},
			"type": "object",
			"properties": {
				"root": {"$ref": "#/$defs/node"}
			}
		}`
		out, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
		var got struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		child := got.Properties["root"]["properties"].(map[string]any)["child"].(map[string]any)
		if child["type"] != "object" || child["description"] != "Child node (See: node)" {
			t.Errorf("cyclic child = %#v, want type:object description:\"Child node (See: node)\"", child)
		}
	})

	t.Run("depth cap", func(t *testing.T) {
		var defs []string
		for i := 0; i < maxRefDepth; i++ {
			defs = append(defs, fmt.Sprintf(`"d%d":{"type":"object","properties":{"next":{"$ref":"#/$defs/d%d"}}}`, i, i+1))
		}
		defs = append(defs, fmt.Sprintf(`"d%d":{"type":"string","nullable":true,"description":"deep leaf"}`, maxRefDepth))
		in := `{"type":"object","$defs":{` + strings.Join(defs, ",") + `},"properties":{"x":{"$ref":"#/$defs/d0"}}}`

		out, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		cur := m["properties"].(map[string]any)["x"].(map[string]any)
		for range maxRefDepth {
			cur = cur["properties"].(map[string]any)["next"].(map[string]any)
		}
		if !reflect.DeepEqual(cur["type"], []any{"string", "null"}) {
			t.Errorf("depth-capped leaf type = %#v, want [string null]", cur["type"])
		}
		wantDesc := fmt.Sprintf("deep leaf (See: d%d)", maxRefDepth)
		if cur["description"] != wantDesc {
			t.Errorf("depth-capped leaf description = %q, want %q", cur["description"], wantDesc)
		}
	})

	t.Run("cycle respects exhausted budget", func(t *testing.T) {
		in := `{
			"$defs": {
				"list": {
					"type": "array",
					"nullable": true,
					"description": "Nested list",
					"items": {"$ref": "#/$defs/list"}
				}
			},
			"type": "object",
			"properties": {
				"root": {"$ref": "#/$defs/list"}
			}
		}`
		out, _ := cleanToolSchema(json.RawMessage(in), false, &refBudget{})
		var got struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"type": "object", "description": "See: list"}
		if !reflect.DeepEqual(got.Properties["root"], want) {
			t.Errorf("root = %#v, want %#v", got.Properties["root"], want)
		}
	})
}

// TestRefFallbackKeepsLiteralKeywords checks that when a $ref is refused (missing, cyclic, or
// over budget), sibling literal keywords (default, examples, enum) are copied literally rather
// than rewritten by resolveRefs.
func TestRefFallbackKeepsLiteralKeywords(t *testing.T) {
	t.Run("missing ref keeps literal default examples and enum", func(t *testing.T) {
		in := `{
			"$defs": {
				"existing": {"type": "string", "description": "must not be expanded into literals"}
			},
			"type": "object",
			"properties": {
				"withDefault": {
					"$ref": "#/$defs/missing",
					"default": {"$ref": "literal-not-ref", "$defs": {"k": 1}},
					"examples": [{"$ref": "#/$defs/existing"}]
				},
				"withEnum": {
					"$ref": "#/$defs/missing",
					"enum": [{"$ref": "literal-enum"}]
				}
			}
		}`
		out, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
		var got struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}

		wd := got.Properties["withDefault"]
		wantDefault := map[string]any{"$ref": "literal-not-ref", "$defs": map[string]any{"k": float64(1)}}
		if !reflect.DeepEqual(wd["default"], wantDefault) {
			t.Errorf("withDefault.default = %#v, want %#v", wd["default"], wantDefault)
		}
		wantExamples := []any{map[string]any{"$ref": "#/$defs/existing"}}
		if !reflect.DeepEqual(wd["examples"], wantExamples) {
			t.Errorf("withDefault.examples = %#v, want %#v", wd["examples"], wantExamples)
		}

		we := got.Properties["withEnum"]
		wantEnum := []any{`{"$ref":"literal-enum"}`}
		if !reflect.DeepEqual(we["enum"], wantEnum) {
			t.Errorf("withEnum.enum = %#v, want %#v", we["enum"], wantEnum)
		}

		if again, changed := cleanToolSchema(out, false, newRefBudget()); changed {
			t.Errorf("second pass changed the schema:\n%s\n%s", out, again)
		}
	})

	t.Run("cyclic ref keeps literal default", func(t *testing.T) {
		in := `{
			"$defs": {
				"node": {
					"type": "object",
					"properties": {
						"child": {
							"$ref": "#/$defs/node",
							"default": {"$ref": "#/$defs/node"}
						}
					}
				}
			},
			"type": "object",
			"properties": {
				"root": {"$ref": "#/$defs/node"}
			}
		}`
		out, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
		var got struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		child := got.Properties["root"]["properties"].(map[string]any)["child"].(map[string]any)
		wantDefault := map[string]any{"$ref": "#/$defs/node"}
		if !reflect.DeepEqual(child["default"], wantDefault) {
			t.Errorf("cyclic child.default = %#v, want %#v", child["default"], wantDefault)
		}
	})

	t.Run("budget-exhausted ref keeps literal default", func(t *testing.T) {
		in := `{
			"$defs": {
				"target": {"type": "string", "description": "expanded target"}
			},
			"type": "object",
			"properties": {
				"p": {
					"$ref": "#/$defs/target",
					"default": {"$ref": "#/$defs/target"}
				}
			}
		}`
		out, _ := cleanToolSchema(json.RawMessage(in), false, &refBudget{})
		var got struct {
			Properties map[string]map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		wantDefault := map[string]any{"$ref": "#/$defs/target"}
		if !reflect.DeepEqual(got.Properties["p"]["default"], wantDefault) {
			t.Errorf("budget-exhausted p.default = %#v, want %#v", got.Properties["p"]["default"], wantDefault)
		}
	})
}
