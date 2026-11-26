package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/extensions/gemini/geminitest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// TestRequestPathCleansToolSchema sends a request with $ref/$defs, a boolean subschema, and a root
// anyOf through generate against an httptest upstream for both Gemini API and Vertex AI and checks
// that the upstream receives a cleaned object schema without those unsupported constructs.
func TestRequestPathCleansToolSchema(t *testing.T) {
	rawSchema := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs": {
			"target": {
				"type": "object",
				"properties": {
					"path": {"type": "string"},
					"tags": {"type": "array", "items": true}
				},
				"required": ["path"]
			}
		},
		"anyOf": [
			{
				"type": "object",
				"properties": {
					"action": {"const": "read"},
					"target": {"$ref": "#/$defs/target"}
				},
				"required": ["action", "target"]
			},
			{
				"type": "object",
				"properties": {
					"action": {"const": "list"},
					"recursive": {"type": "boolean"}
				},
				"required": ["action"]
			}
		]
	}`

	for _, kind := range []string{kindGemini, kindVertexSA} {
		t.Run(kind, func(t *testing.T) {
			tokens = &tokenCache{entries: map[string]*tokenEntry{}}
			srv := geminitest.New(t)
			var gotBody []byte
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				gotBody = req.Body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"message":"stop","status":"INVALID_ARGUMENT"}}`))
			}
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
			if kind == kindVertexSA {
				cred = &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Settings: settingsFor(srv, kind), Credential: cred},
				Request: llm.Request{
					Model:  "gemini-2.5-pro",
					Native: &llm.Native{Format: llm.FormatOpenAIChat},
					Items: []llm.Item{{
						Type:    llm.ItemTypeMessage,
						Role:    llm.RoleUser,
						Content: []llm.Part{{Type: llm.PartText, Text: "run tool"}},
					}},
					Tools: []llm.Tool{{
						Type:        llm.ToolFunction,
						Name:        "inspect_workspace",
						Description: "Inspect files",
						Parameters:  json.RawMessage(rawSchema),
					}},
				},
			}
			if err := generate(context.Background(), call, nil); err == nil {
				t.Fatal("generate succeeded against a server that refuses")
			}
			if len(gotBody) == 0 {
				t.Fatal("no request reached upstream")
			}

			var sent struct {
				Tools []struct {
					FunctionDeclarations []struct {
						Name                 string         `json:"name"`
						ParametersJSONSchema map[string]any `json:"parametersJsonSchema"`
					} `json:"functionDeclarations"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(gotBody, &sent); err != nil {
				t.Fatalf("unmarshal upstream body: %v\nbody: %s", err, gotBody)
			}
			if len(sent.Tools) != 1 || len(sent.Tools[0].FunctionDeclarations) != 1 {
				t.Fatalf("unexpected tools in upstream body: %s", gotBody)
			}
			schema := sent.Tools[0].FunctionDeclarations[0].ParametersJSONSchema
			if schema["type"] != "object" {
				t.Errorf("root type = %v, want \"object\" (schema: %s)", schema["type"], gotBody)
			}
			for _, forbidden := range []string{"$schema", "$defs", "anyOf", "oneOf"} {
				if _, has := schema[forbidden]; has {
					t.Errorf("root schema still has %q: %s", forbidden, gotBody)
				}
			}
			props, _ := schema["properties"].(map[string]any)
			if props == nil {
				t.Fatalf("root schema has no properties: %s", gotBody)
			}
			if reqList, _ := schema["required"].([]any); len(reqList) != 1 || reqList[0] != "action" {
				t.Errorf("root required = %#v, want [\"action\"]", schema["required"])
			}
			if action, _ := props["action"].(map[string]any); !reflect.DeepEqual(action["enum"], []any{"read", "list"}) {
				t.Errorf("action = %#v, want enum [read list] from both branches", props["action"])
			}
			target, _ := props["target"].(map[string]any)
			if target == nil || target["type"] != "object" {
				t.Fatalf("inlined target property missing or not an object: %#v", props["target"])
			}
			if _, hasRef := target["$ref"]; hasRef {
				t.Errorf("target property still has $ref: %#v", target)
			}
			targetProps, _ := target["properties"].(map[string]any)
			tags, _ := targetProps["tags"].(map[string]any)
			if tags == nil {
				t.Fatalf("tags property missing: %#v", targetProps)
			}
			if _, ok := tags["items"].(map[string]any); !ok {
				t.Errorf("tags.items = %T (%v), want object schema (boolean subschema must be normalized)", tags["items"], tags["items"])
			}
		})
	}
}

func TestCountTokensCleansToolSchema(t *testing.T) {
	for _, kind := range []string{kindGemini, kindVertexSA} {
		t.Run(kind, func(t *testing.T) {
			tokens = &tokenCache{entries: map[string]*tokenEntry{}}
			srv := geminitest.New(t)
			var gotBody []byte
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				gotBody = req.Body
				w.WriteHeader(http.StatusBadRequest)
			}
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
			if kind == kindVertexSA {
				cred = &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Settings: settingsFor(srv, kind), Credential: cred},
				Request: llm.Request{
					Model: "gemini-2.5-pro",
					Native: &llm.Native{
						Format: llm.FormatGeminiGenerate,
						Body: []byte(`{
							"contents":[{"role":"user","parts":[{"text":"hi"}]}],
							"tools":[{"functionDeclarations":[{"name":"inspect","parametersJsonSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","properties":{"x":{"items":true}}}}]}]
						}`),
					},
				},
			}
			_ = countTokens(context.Background(), call, nil)
			if len(gotBody) == 0 {
				t.Fatal("no request reached upstream")
			}
			s := string(gotBody)
			if strings.Contains(s, "$schema") {
				t.Errorf("countTokens kept $schema: %s", s)
			}
			if !strings.Contains(s, `"items":{}`) {
				t.Errorf("countTokens did not normalize boolean items subschema: %s", s)
			}
		})
	}
}

func TestCleanUnknownKeywordsAndLiteralProtection(t *testing.T) {
	in := `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "https://example.test/schema",
		"$anchor": "root",
		"$comment": "strip me",
		"title": "Kept Title",
		"type": "object",
		"readOnly": true,
		"writeOnly": false,
		"deprecated": true,
		"x-internal": {"foo": "bar"},
		"additionalProperties": false,
		"properties": {
			"properties": {"type": "string", "title": "Property named properties", "x-prop": 1},
			"readOnly": {"type": "boolean", "readOnly": true, "description": "Valid readOnly field"},
			"writeOnly": {"type": "boolean", "writeOnly": true},
			"$ref": {"type": "string"},
			"x-custom": {"type": "string", "minLength": "2"},
			"meta": {
				"type": "object",
				"default": {"$ref": "literal-not-ref", "x-keep": true, "readOnly": true},
				"examples": [{"x-keep": 1, "$schema": "kept-inside-example"}]
			}
		},
		"required": ["properties", "readOnly", "writeOnly", "$ref", "x-custom", "meta"]
	}`

	cleaned, changed := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
	if !changed {
		t.Fatal("expected schema to be modified")
	}
	var m map[string]any
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatal(err)
	}
	for _, stripped := range []string{"$schema", "$id", "$anchor", "$comment", "readOnly", "writeOnly", "deprecated", "x-internal"} {
		if _, has := m[stripped]; has {
			t.Errorf("keyword %q should be stripped from root: %s", stripped, cleaned)
		}
	}
	if m["title"] != "Kept Title" {
		t.Errorf("title = %v, want \"Kept Title\"", m["title"])
	}
	if m["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false preserved on parametersJsonSchema", m["additionalProperties"])
	}
	props := m["properties"].(map[string]any)
	for _, name := range []string{"properties", "readOnly", "writeOnly", "$ref", "x-custom", "meta"} {
		if _, ok := props[name]; !ok {
			t.Errorf("legitimate property %q was stripped: %s", name, cleaned)
		}
	}
	if xProp := props["properties"].(map[string]any); xProp["title"] != "Property named properties" {
		t.Errorf("property named 'properties' lost title: %#v", xProp)
	} else if _, has := xProp["x-prop"]; has {
		t.Errorf("keyword x-prop inside property named 'properties' should be stripped: %#v", xProp)
	}
	if xc := props["x-custom"].(map[string]any); xc["minLength"] != float64(2) {
		t.Errorf("minLength = %#v (%T), want number 2", xc["minLength"], xc["minLength"])
	}
	meta := props["meta"].(map[string]any)
	def := meta["default"].(map[string]any)
	if def["$ref"] != "literal-not-ref" || def["x-keep"] != true || def["readOnly"] != true {
		t.Errorf("literal default object was mutated: %#v", def)
	}
	ex := meta["examples"].([]any)[0].(map[string]any)
	if ex["x-keep"] != float64(1) || ex["$schema"] != "kept-inside-example" {
		t.Errorf("literal examples value was mutated: %#v", ex)
	}

	if _, again := cleanToolSchema(cleaned, false, newRefBudget()); again {
		t.Errorf("expected second pass to be a no-op")
	}
}

func TestCleanRefsCyclesAndNoAliasing(t *testing.T) {
	in := `{
		"$defs": {
			"leaf~1slash": {"type": "string", "description": "Original"},
			"node": {
				"type": "object",
				"properties": {
					"value": {"type": "string"},
					"next": {"$ref": "#/$defs/node"}
				},
				"required": ["value"]
			}
		},
		"type": "object",
		"properties": {
			"a": {"$ref": "#/$defs/leaf~01slash", "description": "Override A"},
			"b": {"$ref": "#/$defs/leaf~01slash"},
			"tree": {"$ref": "#/$defs/node"},
			"missing": {"$ref": "#/$defs/does_not_exist"}
		}
	}`
	in = strings.ReplaceAll(in, "leaf~1slash", "leaf/slash")
	in = strings.ReplaceAll(in, "leaf~01slash", "leaf~1slash")

	cleaned, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
	var m map[string]any
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["$defs"]; has {
		t.Fatalf("$defs not stripped: %s", cleaned)
	}
	props := m["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	b := props["b"].(map[string]any)
	if a["description"] != "Override A" || b["description"] != "Original" {
		t.Errorf("shared ref aliased or wrong override: a=%#v b=%#v", a, b)
	}
	tree := props["tree"].(map[string]any)
	next := tree["properties"].(map[string]any)["next"].(map[string]any)
	if _, hasRef := next["$ref"]; hasRef {
		t.Errorf("cyclic $ref survived: %#v", next)
	}
	if next["type"] != "object" {
		t.Errorf("cyclic fallback = %#v, want type:object", next)
	}
	missing := props["missing"].(map[string]any)
	if missing["type"] != "object" {
		t.Errorf("missing $ref fallback = %#v, want type:object", missing)
	}
}

func TestCleanConditionalsAndAllOf(t *testing.T) {
	in := `{
		"type": "object",
		"properties": {
			"ids": {"type": "array", "allOf": [{"items": {"type": "integer"}}]}
		},
		"if": {"properties": {"mode": {"const": "strict"}}},
		"then": {"properties": {"strictLevel": {"type": "integer"}, "ids": {"type": "string"}}, "required": ["strictLevel"]},
		"else": {"properties": {"fallback": {"type": "boolean"}}},
		"allOf": [
			{"properties": {"env": {"type": "string"}}, "required": ["env"]}
		]
	}`

	cleaned, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
	var m map[string]any
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{"if", "then", "else", "allOf"} {
		if _, has := m[kw]; has {
			t.Errorf("%q survived: %s", kw, cleaned)
		}
	}
	props := m["properties"].(map[string]any)
	ids := props["ids"].(map[string]any)
	if ids["type"] != "array" || ids["items"].(map[string]any)["type"] != "integer" {
		t.Errorf("ids = %#v, want array of integer", ids)
	}
	if _, has := props["strictLevel"]; !has {
		t.Errorf("then property strictLevel missing: %s", cleaned)
	}
	if _, has := props["fallback"]; !has {
		t.Errorf("else property fallback missing: %s", cleaned)
	}
	if _, has := props["env"]; !has {
		t.Errorf("allOf property env missing: %s", cleaned)
	}
	req, _ := m["required"].([]any)
	if !reflect.DeepEqual(req, []any{"env"}) {
		t.Errorf("required = %#v, want [\"env\"]", req)
	}
}

func TestCleanTypeRequiredAndArrayItemsConsistency(t *testing.T) {
	in := `{
		"properties": {
			"nestedBare": {
				"alpha": {"type": "string", "required": true},
				"beta": {"type": "string", "items": {"type": "number"}, "required": false}
			},
			"missingItems": {"type": "array"},
			"untypedArray": {"items": {"type": "integer"}},
			"tupleArray": {"items": [{"type": "string"}, true]},
			"wrongTypedObj": {"type": "string", "properties": {"k": {"type": "string"}}, "required": ["k", "orphan"]},
			"orphanReqOnly": {"type": "object", "required": ["ghost"]}
		},
		"required": ["nestedBare", "nestedBare", "nonexistent"]
	}`

	cleaned, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
	var m map[string]any
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatal(err)
	}
	if m["type"] != "object" {
		t.Errorf("root type = %v, want object", m["type"])
	}
	if !reflect.DeepEqual(m["required"], []any{"nestedBare"}) {
		t.Errorf("root required = %#v, want [\"nestedBare\"]", m["required"])
	}
	props := m["properties"].(map[string]any)

	bare := props["nestedBare"].(map[string]any)
	if bare["type"] != "object" || !reflect.DeepEqual(bare["required"], []any{"alpha"}) {
		t.Errorf("nestedBare = %#v, want type:object required:[alpha]", bare)
	}
	beta := bare["properties"].(map[string]any)["beta"].(map[string]any)
	if _, has := beta["items"]; has {
		t.Errorf("string property beta still has items: %#v", beta)
	}
	if _, has := beta["required"]; has {
		t.Errorf("child property still has boolean required: %#v", beta)
	}

	missingItems := props["missingItems"].(map[string]any)
	if items, ok := missingItems["items"].(map[string]any); !ok || items["type"] != "string" {
		t.Errorf("missingItems.items = %#v, want {type:string}", missingItems["items"])
	}

	untyped := props["untypedArray"].(map[string]any)
	if untyped["type"] != "array" {
		t.Errorf("untypedArray.type = %v, want array", untyped["type"])
	}

	tuple := props["tupleArray"].(map[string]any)
	if tuple["type"] != "array" {
		t.Errorf("tupleArray.type = %v, want array", tuple["type"])
	}
	if pi, _ := tuple["prefixItems"].([]any); len(pi) != 2 {
		t.Errorf("tupleArray.prefixItems = %#v, want 2 elements", tuple["prefixItems"])
	}
	if _, ok := tuple["items"].(map[string]any); !ok {
		t.Errorf("tupleArray.items = %#v, want schema map", tuple["items"])
	}

	wrongObj := props["wrongTypedObj"].(map[string]any)
	if wrongObj["type"] != "object" || !reflect.DeepEqual(wrongObj["required"], []any{"k"}) {
		t.Errorf("wrongTypedObj = %#v, want type:object required:[k]", wrongObj)
	}

	orphanObj := props["orphanReqOnly"].(map[string]any)
	if _, hasReq := orphanObj["required"]; hasReq {
		t.Errorf("orphanReqOnly still has required without properties: %#v", orphanObj)
	}
}

func TestCleanEnumsAndBooleanSubschemas(t *testing.T) {
	in := `{
		"type": "object",
		"properties": {
			"constStr": {"const": "only"},
			"intEnum": {"type": "integer", "enum": [1, 2, 2]},
			"nullableEnum": {"type": "string", "enum": ["a", null]},
			"emptyEnum": {"type": "string", "enum": []},
			"nullOnlyEnum": {"enum": [null]},
			"boolEnum": {"type": "boolean", "enum": [true]},
			"boolProp": true,
			"falseProp": false,
			"unionWithFalse": {
				"anyOf": [false, {"type": "string"}, {"type": "null"}]
			}
		}
	}`

	cleaned, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
	var m map[string]any
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatal(err)
	}
	props := m["properties"].(map[string]any)

	cs := props["constStr"].(map[string]any)
	if _, has := cs["const"]; has || !reflect.DeepEqual(cs["enum"], []any{"only"}) {
		t.Errorf("constStr = %#v, want enum:[only]", cs)
	}

	ie := props["intEnum"].(map[string]any)
	if ie["type"] != "integer" || !reflect.DeepEqual(ie["enum"], []any{float64(1), float64(2)}) {
		t.Errorf("intEnum = %#v, want type:integer enum:[1 2] (numbers)", ie)
	}

	ne := props["nullableEnum"].(map[string]any)
	if !reflect.DeepEqual(ne["enum"], []any{"a"}) || !reflect.DeepEqual(ne["type"], []any{"string", "null"}) {
		t.Errorf("nullableEnum = %#v", ne)
	}

	ee := props["emptyEnum"].(map[string]any)
	if _, has := ee["enum"]; has {
		t.Errorf("emptyEnum kept empty enum: %#v", ee)
	}

	no := props["nullOnlyEnum"].(map[string]any)
	if _, has := no["enum"]; has {
		t.Errorf("nullOnlyEnum kept enum: %#v", no)
	}

	be := props["boolEnum"].(map[string]any)
	if be["type"] != "boolean" || be["enum"] != nil || !strings.Contains(be["description"].(string), "Allowed: true") {
		t.Errorf("boolEnum = %#v", be)
	}

	if bp, ok := props["boolProp"].(map[string]any); !ok || len(bp) != 0 {
		t.Errorf("boolProp = %#v, want {}", props["boolProp"])
	}
	if fp, ok := props["falseProp"].(map[string]any); !ok || len(fp) != 0 {
		t.Errorf("falseProp = %#v, want {}", props["falseProp"])
	}

	uf := props["unionWithFalse"].(map[string]any)
	if !reflect.DeepEqual(uf["type"], []any{"string", "null"}) || uf["anyOf"] != nil {
		t.Errorf("unionWithFalse = %#v, want unwrapped nullable string", uf)
	}
}

func TestCleanUnionsAndOpenAPIPassthrough(t *testing.T) {
	// Nested oneOf normalizes to anyOf, and openAPI parameters preserve uppercase OBJECT/STRING.
	in := `{"type":"object","properties":{"a":{"oneOf":[{"type":"string"},{"type":"integer"}]}}}`
	cleaned, _ := cleanToolSchema(json.RawMessage(in), false, newRefBudget())
	var m map[string]any
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatal(err)
	}
	nestedA := m["properties"].(map[string]any)["a"].(map[string]any)
	if _, hasOneOf := nestedA["oneOf"]; hasOneOf || len(nestedA["anyOf"].([]any)) != 2 {
		t.Errorf("nested oneOf not normalized to anyOf: %#v", nestedA)
	}

	cleanOpenAPI := json.RawMessage(`{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}`)
	out, changed := cleanToolSchema(cleanOpenAPI, true, newRefBudget())
	if changed || string(out) != string(cleanOpenAPI) {
		t.Errorf("clean OpenAPI schema changed = %v, out = %s", changed, out)
	}

	dirtyOpenAPI := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"nick":{"type":["string","null"]}}}`)
	oaOut, oaChanged := cleanToolSchema(dirtyOpenAPI, true, newRefBudget())
	if !oaChanged {
		t.Fatal("expected dirty OpenAPI schema to change")
	}
	var oaMap map[string]any
	_ = json.Unmarshal(oaOut, &oaMap)
	if _, hasAP := oaMap["additionalProperties"]; hasAP {
		t.Errorf("OpenAPI kept additionalProperties: %s", oaOut)
	}
	nick := oaMap["properties"].(map[string]any)["nick"].(map[string]any)
	if nick["type"] != "string" || nick["nullable"] != true {
		t.Errorf("OpenAPI nullable nick = %#v", nick)
	}
}

// TestRootUnionKeepsEveryBranchsValues checks how a tool's root anyOf/oneOf is flattened into one
// object when a property appears in more than one branch.
func TestRootUnionKeepsEveryBranchsValues(t *testing.T) {
	root := func(parentProps string, branches ...string) string {
		props := ""
		if parentProps != "" {
			props = `"properties": {` + parentProps + `},`
		}
		return `{"type":"object",` + props + `"%KEY%":[` + strings.Join(branches, ",") + `]}`
	}
	branch := func(props string) string {
		return `{"type":"object","properties":{` + props + `}}`
	}
	cases := []struct {
		name string
		in   string
		want string // the property "p" after cleaning, as JSON
		// wantOpenAPI is want for OpenAPI mode, when it differs.
		wantOpenAPI string
	}{
		{
			name: "consts of different branches are unioned",
			in:   root("", branch(`"p":{"const":"read"}`), branch(`"p":{"const":"list"}`)),
			want: `{"enum":["read","list"],"type":"string"}`,
		},
		{
			name: "identical schemas keep one",
			in:   root("", branch(`"p":{"type":"integer","minimum":1}`), branch(`"p":{"type":"integer","minimum":1}`)),
			want: `{"minimum":1,"type":"integer"}`,
		},
		{
			name: "values keep first-seen order without duplicates, first non-empty description wins",
			in: root("",
				branch(`"p":{"type":"string","enum":["b","a"]}`),
				branch(`"p":{"type":"string","enum":["a","c"],"description":"second"}`),
				branch(`"p":{"type":"string","enum":["c","d","b"],"description":"third"}`)),
			want: `{"description":"second","enum":["b","a","c","d"],"type":"string"}`,
		},
		{
			name: "a branch that lacks the property does not matter",
			in:   root("", branch(`"p":{"const":"read"}`), branch(`"q":{"type":"string"}`), branch(`"p":{"const":"list"}`)),
			want: `{"enum":["read","list"],"type":"string"}`,
		},
		{
			name: "different types become an anyOf in branch order",
			in:   root("", branch(`"p":{"type":"string"}`), branch(`"p":{"type":"integer"}`), branch(`"p":{"type":"string"}`)),
			want: `{"anyOf":[{"type":"string"},{"type":"integer"}]}`,
		},
		{
			name: "a branch without an enum becomes an anyOf",
			in:   root("", branch(`"p":{"const":"read"}`), branch(`"p":{"type":"string"}`)),
			want: `{"anyOf":[{"enum":["read"],"type":"string"},{"type":"string"}]}`,
		},
		{
			name:        "a null branch and a string branch become one nullable schema",
			in:          root("", branch(`"p":{"type":"null"}`), branch(`"p":{"type":"string"}`)),
			want:        `{"type":["string","null"]}`,
			wantOpenAPI: `{"nullable":true,"type":"string"}`,
		},
		{
			name:        "a null branch and a string enum branch keep nullability",
			in:          root("", branch(`"p":{"type":"null"}`), branch(`"p":{"type":"string","enum":["a"]}`)),
			want:        `{"enum":["a"],"type":["string","null"]}`,
			wantOpenAPI: `{"enum":["a"],"nullable":true,"type":"string"}`,
		},
		{
			name: "the parent's own schema wins",
			in:   root(`"p":{"type":"string","description":"parent"}`, branch(`"p":{"const":"read"}`), branch(`"p":{"const":"list"}`)),
			want: `{"description":"parent","type":"string"}`,
		},
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, openAPI := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(key+"/"+map[bool]string{false: "json", true: "openapi"}[openAPI]+"/"+tc.name, func(t *testing.T) {
					in := strings.ReplaceAll(tc.in, "%KEY%", key)
					first, changed := cleanToolSchema(json.RawMessage(in), openAPI, newRefBudget())
					if !changed {
						t.Fatal("schema unchanged")
					}
					var m map[string]any
					if err := json.Unmarshal(first, &m); err != nil {
						t.Fatal(err)
					}
					if _, has := m[key]; has || m["type"] != "object" {
						t.Fatalf("root not flattened: %s", first)
					}
					got, _ := json.Marshal(m["properties"].(map[string]any)["p"])
					want := tc.want
					if openAPI && tc.wantOpenAPI != "" {
						want = tc.wantOpenAPI
					}
					if string(got) != want {
						t.Errorf("p = %s, want %s", got, want)
					}
					if again, c := cleanToolSchema(first, openAPI, newRefBudget()); c {
						t.Errorf("second pass changed the schema:\n%s\n%s", first, again)
					}
					for range 20 {
						if next, _ := cleanToolSchema(json.RawMessage(in), openAPI, newRefBudget()); string(next) != string(first) {
							t.Fatalf("output varies between runs:\n%s\n%s", first, next)
						}
					}
				})
			}
		}
	}
}

// TestNumericEnums checks that JSON Schema mode (parametersJsonSchema) keeps an enum of numbers
// and its type, and that OpenAPI mode (parameters), whose enum is a list of strings, still turns it
// into a string enum.
func TestNumericEnums(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		json    string // the property "p" after cleaning in JSON Schema mode
		openAPI string // and in OpenAPI mode
	}{
		{
			name:    "integer",
			in:      `{"type":"integer","enum":[1,2,3]}`,
			json:    `{"enum":[1,2,3],"type":"integer"}`,
			openAPI: `{"enum":["1","2","3"],"type":"string"}`,
		},
		{
			name:    "number keeps decimals",
			in:      `{"type":"number","enum":[0.5,1,2.25]}`,
			json:    `{"enum":[0.5,1,2.25],"type":"number"}`,
			openAPI: `{"enum":["0.5","1","2.25"],"type":"string"}`,
		},
		{
			name:    "duplicates are removed by their text",
			in:      `{"type":"integer","enum":[1,2,1,2,3]}`,
			json:    `{"enum":[1,2,3],"type":"integer"}`,
			openAPI: `{"enum":["1","2","3"],"type":"string"}`,
		},
		{
			name:    "a null goes through makeNullable",
			in:      `{"type":"integer","enum":[1,null,2]}`,
			json:    `{"enum":[1,2],"type":["integer","null"]}`,
			openAPI: `{"enum":["1","2"],"nullable":true,"type":"string"}`,
		},
		{
			name:    "a type list with one non-null member",
			in:      `{"type":["number","null"],"enum":[1.5,2]}`,
			json:    `{"enum":[1.5,2],"type":["number","null"]}`,
			openAPI: `{"enum":["1.5","2"],"nullable":true,"type":"string"}`,
		},
		{
			name:    "no type, whole numbers: integer",
			in:      `{"enum":[1,2]}`,
			json:    `{"enum":[1,2],"type":"integer"}`,
			openAPI: `{"enum":["1","2"],"type":"string"}`,
		},
		{
			name:    "no type, a decimal point: number",
			in:      `{"enum":[1,2.5]}`,
			json:    `{"enum":[1,2.5],"type":"number"}`,
			openAPI: `{"enum":["1","2.5"],"type":"string"}`,
		},
		{
			name:    "no type, an exponent: number",
			in:      `{"enum":[1,2e3]}`,
			json:    `{"enum":[1,2e3],"type":"number"}`,
			openAPI: `{"enum":["1","2e3"],"type":"string"}`,
		},
		{
			name:    "no type with a null",
			in:      `{"enum":[1,null]}`,
			json:    `{"enum":[1],"type":["integer","null"]}`,
			openAPI: `{"enum":["1"],"nullable":true,"type":"string"}`,
		},
		{
			name:    "const",
			in:      `{"type":"integer","const":7}`,
			json:    `{"enum":[7],"type":"integer"}`,
			openAPI: `{"enum":["7"],"type":"string"}`,
		},
		{
			name:    "numbers under type string stay strings",
			in:      `{"type":"string","enum":[1,2]}`,
			json:    `{"enum":["1","2"],"type":"string"}`,
			openAPI: `{"enum":["1","2"],"type":"string"}`,
		},
		{
			name:    "mixed values become strings",
			in:      `{"type":"integer","enum":[1,"a"]}`,
			json:    `{"enum":["1","a"],"type":"string"}`,
			openAPI: `{"enum":["1","a"],"type":"string"}`,
		},
		{
			name:    "strings stay strings",
			in:      `{"type":"string","enum":["a","b","a"]}`,
			json:    `{"enum":["a","b"],"type":"string"}`,
			openAPI: `{"enum":["a","b"],"type":"string"}`,
		},
		{
			name:    "nullable string enum keeps null across passes",
			in:      `{"type":"string","enum":["a",null]}`,
			json:    `{"enum":["a"],"type":["string","null"]}`,
			openAPI: `{"enum":["a"],"nullable":true,"type":"string"}`,
		},
		{
			name:    "string type list with null is kept",
			in:      `{"type":["string","null"],"enum":["a"]}`,
			json:    `{"enum":["a"],"type":["string","null"]}`,
			openAPI: `{"enum":["a"],"nullable":true,"type":"string"}`,
		},
		{
			name:    "uppercase string type list with null is kept",
			in:      `{"type":["STRING","null"],"enum":["a"]}`,
			json:    `{"enum":["a"],"type":["STRING","null"]}`,
			openAPI: `{"enum":["a"],"nullable":true,"type":"STRING"}`,
		},
		{
			name:    "no type, booleans become strings",
			in:      `{"enum":[true,false]}`,
			json:    `{"enum":["true","false"],"type":"string"}`,
			openAPI: `{"enum":["true","false"],"type":"string"}`,
		},
	}
	for _, tc := range cases {
		for _, openAPI := range []bool{false, true} {
			want := tc.json
			mode := "json"
			if openAPI {
				want, mode = tc.openAPI, "openapi"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				in := `{"type":"object","properties":{"p":` + tc.in + `}}`
				first, _ := cleanToolSchema(json.RawMessage(in), openAPI, newRefBudget())
				// Numbers are decoded as json.Number so that 2e3 is compared as written.
				var m struct {
					Properties map[string]any `json:"properties"`
				}
				dec := json.NewDecoder(bytes.NewReader(first))
				dec.UseNumber()
				if err := dec.Decode(&m); err != nil {
					t.Fatal(err)
				}
				if got, _ := json.Marshal(m.Properties["p"]); string(got) != want {
					t.Errorf("p = %s, want %s", got, want)
				}
				if again, changed := cleanToolSchema(first, openAPI, newRefBudget()); changed {
					t.Errorf("second pass changed the schema:\n%s\n%s", first, again)
				}
			})
		}
	}
}

// TestRequestPathKeepsNumericEnum sends a Chat Completions tool with an integer enum and checks
// that the mock upstream receives the type and the numbers in parametersJsonSchema. That Google's
// own API accepts this shape is not checked here.
func TestRequestPathKeepsNumericEnum(t *testing.T) {
	for _, kind := range []string{kindGemini, kindVertexSA} {
		t.Run(kind, func(t *testing.T) {
			tokens = &tokenCache{entries: map[string]*tokenEntry{}}
			srv := geminitest.New(t)
			var gotBody []byte
			srv.Generate = func(w http.ResponseWriter, r *http.Request, req geminitest.Request) {
				gotBody = req.Body
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"code":400,"message":"stop","status":"INVALID_ARGUMENT"}}`))
			}
			cred := &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: srv.IssueKey()}
			if kind == kindVertexSA {
				cred = &protocol.Credential{Kind: CredentialServiceAccount, Data: srv.ServiceAccount("proj-1", true)}
			}
			call := sdk.Call{
				Account: protocol.Account{ID: "a", Settings: settingsFor(srv, kind), Credential: cred},
				Request: llm.Request{
					Model:  "gemini-2.5-pro",
					Native: &llm.Native{Format: llm.FormatOpenAIChat},
					Items: []llm.Item{{
						Type:    llm.ItemTypeMessage,
						Role:    llm.RoleUser,
						Content: []llm.Part{{Type: llm.PartText, Text: "set the level"}},
					}},
					Tools: []llm.Tool{{
						Type: llm.ToolFunction,
						Name: "set_level",
						Parameters: json.RawMessage(`{"type":"object","properties":{
							"level":{"type":"integer","enum":[1,2,3]},
							"scale":{"type":"number","enum":[0.5,1.5]}
						},"required":["level"]}`),
					}},
				},
			}
			if err := generate(context.Background(), call, nil); err == nil {
				t.Fatal("generate succeeded against a server that refuses")
			}
			var sent struct {
				Tools []struct {
					FunctionDeclarations []struct {
						ParametersJSONSchema struct {
							Properties map[string]map[string]any `json:"properties"`
						} `json:"parametersJsonSchema"`
					} `json:"functionDeclarations"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(gotBody, &sent); err != nil || len(sent.Tools) != 1 || len(sent.Tools[0].FunctionDeclarations) != 1 {
				t.Fatalf("unexpected upstream body (%v): %s", err, gotBody)
			}
			props := sent.Tools[0].FunctionDeclarations[0].ParametersJSONSchema.Properties
			if got := props["level"]; got["type"] != "integer" || !reflect.DeepEqual(got["enum"], []any{float64(1), float64(2), float64(3)}) {
				t.Errorf("level = %#v, want type integer and enum [1 2 3] as numbers", got)
			}
			if got := props["scale"]; got["type"] != "number" || !reflect.DeepEqual(got["enum"], []any{0.5, 1.5}) {
				t.Errorf("scale = %#v, want type number and enum [0.5 1.5] as numbers", got)
			}
		})
	}
}
