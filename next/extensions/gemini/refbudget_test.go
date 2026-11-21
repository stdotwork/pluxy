package main

import (
	"encoding/json"
	"fmt"
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
