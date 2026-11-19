package main

import (
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
							"tools":[{"functionDeclarations":[{"name":"bad/name","parametersJsonSchema":{"$schema":"https://json-schema.org/draft/2020-12/schema","properties":{"x":{"items":true}}}}]}]
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
			if strings.Contains(s, `"bad/name"`) {
				t.Errorf("countTokens kept invalid function name: %s", s)
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

	cleaned, changed := cleanSchemaBytes(json.RawMessage(in), schemaOpts{isToolParams: true})
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

	// Idempotent.
	if _, again := cleanSchemaBytes(cleaned, schemaOpts{isToolParams: true}); again {
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
	// Fix ~0/~1 pointer cleanly: "#/$defs/leaf~1slash" -> "leaf/slash"; check exact escape below too.
	in = strings.ReplaceAll(in, "leaf~1slash", "leaf/slash")
	in = strings.ReplaceAll(in, "leaf~01slash", "leaf~1slash")

	cleaned, _ := cleanSchemaBytes(json.RawMessage(in), schemaOpts{isToolParams: true})
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

	cleaned, _ := cleanSchemaBytes(json.RawMessage(in), schemaOpts{isToolParams: true})
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
	// Existing ids wins over then.ids, and keeps allOf items integer.
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
	// strictLevel from `then` must NOT become required, while env from `allOf` IS required.
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

	cleaned, _ := cleanSchemaBytes(json.RawMessage(in), schemaOpts{isToolParams: true})
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

	cleaned, _ := cleanSchemaBytes(json.RawMessage(in), schemaOpts{isToolParams: true})
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
	if ie["type"] != "string" || !reflect.DeepEqual(ie["enum"], []any{"1", "2"}) {
		t.Errorf("intEnum = %#v, want type:string enum:[1 2]", ie)
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

func TestCleanUnionsResponseVsToolParamsAndOpenAPI(t *testing.T) {
	// Nested oneOf normalizes to anyOf, and responseJsonSchema keeps root multi-branch unions.
	respIn := `{
		"anyOf": [
			{"type": "object", "properties": {"a": {"oneOf": [{"type": "string"}, {"type": "integer"}]}}},
			{"type": "object", "properties": {"b": {"type": "boolean"}}}
		]
	}`
	respCleaned, _ := cleanSchemaBytes(json.RawMessage(respIn), schemaOpts{isToolParams: false})
	var respMap map[string]any
	if err := json.Unmarshal(respCleaned, &respMap); err != nil {
		t.Fatal(err)
	}
	branches, _ := respMap["anyOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("responseJsonSchema root lost union branches: %s", respCleaned)
	}
	nestedA := branches[0].(map[string]any)["properties"].(map[string]any)["a"].(map[string]any)
	if _, hasOneOf := nestedA["oneOf"]; hasOneOf || len(nestedA["anyOf"].([]any)) != 2 {
		t.Errorf("nested oneOf not normalized to anyOf: %#v", nestedA)
	}

	// OpenAPI parameters preserve uppercase OBJECT/STRING byte-for-byte when already clean.
	cleanOpenAPI := json.RawMessage(`{"type":"OBJECT","properties":{"city":{"type":"STRING"}},"required":["city"]}`)
	out, changed := cleanSchemaBytes(cleanOpenAPI, schemaOpts{isToolParams: true, isOpenAPI: true})
	if changed || string(out) != string(cleanOpenAPI) {
		t.Errorf("clean OpenAPI schema changed = %v, out = %s", changed, out)
	}

	// OpenAPI converts type: ["string", "null"] -> type:"string", nullable:true and hints additionalProperties:false.
	dirtyOpenAPI := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"nick":{"type":["string","null"]}}}`)
	oaOut, oaChanged := cleanSchemaBytes(dirtyOpenAPI, schemaOpts{isToolParams: true, isOpenAPI: true})
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

func TestFunctionNameSanitizationAndRestore(t *testing.T) {
	// Valid names stay untouched on both Gemini API and Vertex AI.
	if m, _ := buildFunctionNameMap([]string{"get_weather.v1:read-all"}, nil, true); len(m) != 0 {
		t.Errorf("valid Vertex name was modified: %#v", m)
	}
	if m, _ := buildFunctionNameMap([]string{"0_valid_on_gemini"}, nil, false); len(m) != 0 {
		t.Errorf("valid Gemini name was modified: %#v", m)
	}

	longVertex := strings.Repeat("a", 80)
	longGemini := strings.Repeat("b", 140)

	body := []byte(`{
		"contents":[
			{"role":"model","parts":[{"functionCall":{"name":"mcp/tool name","args":{}}},{"functionCall":{"name":"1starts_with_digit","args":{}}}]},
			{"role":"user","parts":[{"functionResponse":{"name":"mcp/tool name","response":{}}}]}
		],
		"tools":[{"functionDeclarations":[
			{"name":"mcp/tool name","parametersJsonSchema":{"type":"object"}},
			{"name":"mcp_tool_name","parametersJsonSchema":{"type":"object"}},
			{"name":"1starts_with_digit","parametersJsonSchema":{"type":"object"}},
			{"name":"` + longVertex + `","parametersJsonSchema":{"type":"object"}},
			{"name":"` + longGemini + `","parametersJsonSchema":{"type":"object"}}
		]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["mcp/tool name","` + longVertex + `"]}}
	}`)

	cleaned, restore := cleanRequestSchemas(body, true)
	var req struct {
		Contents []struct {
			Parts []struct {
				FunctionCall     *struct{ Name string } `json:"functionCall"`
				FunctionResponse *struct{ Name string } `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
		Tools []struct {
			FunctionDeclarations []struct {
				Name string `json:"name"`
			} `json:"functionDeclarations"`
		} `json:"tools"`
		ToolConfig struct {
			FunctionCallingConfig struct {
				AllowedFunctionNames []string `json:"allowedFunctionNames"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig"`
	}
	if err := json.Unmarshal(cleaned, &req); err != nil {
		t.Fatal(err)
	}

	decls := req.Tools[0].FunctionDeclarations
	seen := map[string]bool{}
	for _, d := range decls {
		if !validFunctionName(d.Name, true) {
			t.Errorf("sanitized Vertex declaration name %q is still invalid", d.Name)
		}
		if seen[d.Name] {
			t.Errorf("collision in sanitized declaration names: %q", d.Name)
		}
		seen[d.Name] = true
	}
	if decls[1].Name != "mcp_tool_name" {
		t.Errorf("existing valid colliding tool name changed: got %q", decls[1].Name)
	}
	if req.Contents[0].Parts[0].FunctionCall.Name != decls[0].Name ||
		req.Contents[1].Parts[0].FunctionResponse.Name != decls[0].Name ||
		req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != decls[0].Name {
		t.Errorf("references to sanitized function name drifted: decl=%q call=%q resp=%q allow=%q",
			decls[0].Name, req.Contents[0].Parts[0].FunctionCall.Name,
			req.Contents[1].Parts[0].FunctionResponse.Name, req.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0])
	}

	// Gemini API leaves names <=128 starting with digit intact, truncates >128.
	_, gemRestore := cleanRequestSchemas(body, false)
	for mapped, orig := range gemRestore {
		if orig == "1starts_with_digit" {
			t.Errorf("Gemini API should not rewrite 1starts_with_digit (mapped=%q)", mapped)
		}
		if !validFunctionName(mapped, false) {
			t.Errorf("sanitized Gemini name %q invalid", mapped)
		}
	}

	// Verify reporting.Send restores both Item.Name and Raw JSON chunk on tool call reply.
	rec := &recordingSink{}
	rep := &reporting{Stream: rec, restore: restore}
	rawChunk := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"` + decls[0].Name + `","args":{"x":1}}}]},"finishReason":"STOP","index":0}]}`)
	err := rep.Send(llm.Event{
		Type:  llm.EventItemStart,
		Index: 0,
		Item:  &llm.ItemStart{Kind: llm.ItemToolCall, CallID: "c1", Name: decls[0].Name},
		Raw:   rawChunk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	if got := rec.events[0].Item.Name; got != "mcp/tool name" {
		t.Errorf("restored Item.Name = %q, want \"mcp/tool name\"", got)
	}
	if !strings.Contains(string(rec.events[0].Raw), `"name":"mcp/tool name"`) {
		t.Errorf("restored Raw chunk = %s, want original tool name", rec.events[0].Raw)
	}
}

type recordingSink struct {
	events []llm.Event
}

func (r *recordingSink) Send(ev llm.Event) error {
	r.events = append(r.events, ev)
	return nil
}
