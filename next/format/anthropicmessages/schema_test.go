package anthropicmessages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestEncodeStructuredOutputModelGate(t *testing.T) {
	const schema = `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`
	unsupported := []string{
		"claude-3-5-haiku-20241022",
		"claude-3-7-sonnet-20250219",
		"claude-3-7-sonnet-latest",
		"claude-opus-4-20250514",
		"claude-sonnet-4-20250514",
		"claude-opus-4-1-20250805",
		"us.anthropic.claude-3-7-sonnet-20250219-v1:0",
		"claude-opus-4-1@20250805",
	}
	for _, model := range unsupported {
		t.Run("drops on "+model, func(t *testing.T) {
			req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Weather?"}],` +
				`"response_format":{"type":"json_schema","json_schema":{"name":"weather","strict":true,"schema":` + schema + `}}}`))
			if err != nil {
				t.Fatal(err)
			}
			out, losses, err := EncodeRequestWith(req, EncodeOptions{StructuredOutputRules: true})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), `"output_config"`) {
				t.Errorf("body contains output_config for unsupported model %s: %s", model, out)
			}
			if got := lossList(losses); !reflect.DeepEqual(got, []string{"dropped response_format"}) {
				t.Errorf("losses = %q, want [dropped response_format]", got)
			}
		})
	}

	supported := []string{
		"claude-haiku-4-5-20251001",
		"claude-sonnet-4-5-20250929",
		"claude-opus-4-5-20251101",
		"claude-sonnet-4-6",
		"claude-opus-4-6",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-opus-5-5",
		"claude-mythos-preview",
		"claude-test",
		"custom-model",
	}
	for _, model := range supported {
		t.Run("keeps on "+model, func(t *testing.T) {
			req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"Weather?"}],` +
				`"response_format":{"type":"json_schema","json_schema":{"name":"weather","strict":true,"schema":` + schema + `}}}`))
			if err != nil {
				t.Fatal(err)
			}
			out, losses, err := EncodeRequestWith(req, EncodeOptions{StructuredOutputRules: true})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(out, []byte(schema)) {
				t.Errorf("body did not keep valid schema byte-identically for %s: %s", model, out)
			}
			if len(losses) != 0 {
				t.Errorf("losses = %q, want none", lossList(losses))
			}
		})
	}
}

func TestEncodeStructuredOutputSchemaAdaptation(t *testing.T) {
	t.Run("gemini responseSchema adds additionalProperties false", func(t *testing.T) {
		req, err := geminigenerate.ParseRequest("claude-sonnet-4-6", false, []byte(`{
			"contents": [{"role":"user","parts":[{"text":"Weather?"}]}],
			"generationConfig": {
				"responseMimeType": "application/json",
				"responseSchema": {
					"type": "OBJECT",
					"properties": {
						"city": {"type": "STRING"},
						"nested": {
							"type": "OBJECT",
							"properties": {"temp": {"type": "NUMBER"}}
						}
					},
					"required": ["city"]
				}
			}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		out, losses, err := EncodeRequestWith(req, EncodeOptions{StructuredOutputRules: true})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			OutputConfig struct {
				Format struct {
					Type   string          `json:"type"`
					Schema json.RawMessage `json:"schema"`
				} `json:"format"`
			} `json:"output_config"`
		}
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatal(err)
		}
		if body.OutputConfig.Format.Type != "json_schema" {
			t.Fatalf("output_config.format.type = %q, want json_schema", body.OutputConfig.Format.Type)
		}
		sameJSON(t, "schema", body.OutputConfig.Format.Schema, `{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"city": {"type": "string"},
				"nested": {
					"type": "object",
					"additionalProperties": false,
					"properties": {"temp": {"type": "number"}}
				}
			},
			"required": ["city"]
		}`)
		want := []string{
			"approximated response_format.schema.additionalProperties",
			"approximated response_format.schema.properties.nested.additionalProperties",
		}
		if got := lossList(losses); !reflect.DeepEqual(got, want) {
			t.Errorf("losses:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("unsupported constraints dropped and oneOf rewritten to anyOf", func(t *testing.T) {
		req, err := openaichat.ParseRequest([]byte(`{
			"model": "claude-sonnet-4-6",
			"messages": [{"role":"user","content":"Pick"}],
			"response_format": {
				"type": "json_schema",
				"json_schema": {
					"name": "pick",
					"schema": {
						"type": "object",
						"additionalProperties": false,
						"properties": {
							"n": {"type": "integer", "minimum": 1, "maximum": 10, "multipleOf": 2},
							"s": {"type": "string", "minLength": 1, "maxLength": 5, "format": "color", "pattern": "\\bfoo\\b"},
							"email": {"type": "string", "format": "email", "pattern": "^[a-z]+$"},
							"tags": {"type": "array", "items": {"type": "string"}, "minItems": 2, "maxItems": 5, "uniqueItems": true},
							"ok_arr": {"type": "array", "items": {"type": "string"}, "minItems": 1},
							"choice": {"oneOf": [{"type": "string"}, {"type": "integer"}]}
						}
					}
				}
			}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		out, losses, err := EncodeRequestWith(req, EncodeOptions{StructuredOutputRules: true})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			OutputConfig struct {
				Format struct {
					Schema json.RawMessage `json:"schema"`
				} `json:"format"`
			} `json:"output_config"`
		}
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatal(err)
		}
		sameJSON(t, "schema", body.OutputConfig.Format.Schema, `{
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"n": {"type": "integer"},
				"s": {"type": "string"},
				"email": {"type": "string", "format": "email", "pattern": "^[a-z]+$"},
				"tags": {"type": "array", "items": {"type": "string"}},
				"ok_arr": {"type": "array", "items": {"type": "string"}, "minItems": 1},
				"choice": {"anyOf": [{"type": "string"}, {"type": "integer"}]}
			}
		}`)
		want := []string{
			"approximated response_format.schema.properties.choice.oneOf",
			"dropped response_format.schema.properties.n.maximum",
			"dropped response_format.schema.properties.n.minimum",
			"dropped response_format.schema.properties.n.multipleOf",
			"dropped response_format.schema.properties.s.format",
			"dropped response_format.schema.properties.s.maxLength",
			"dropped response_format.schema.properties.s.minLength",
			"dropped response_format.schema.properties.s.pattern",
			"dropped response_format.schema.properties.tags.maxItems",
			"dropped response_format.schema.properties.tags.minItems",
			"dropped response_format.schema.properties.tags.uniqueItems",
		}
		if got := lossList(losses); !reflect.DeepEqual(got, want) {
			t.Errorf("losses:\n got %q\nwant %q", got, want)
		}
	})

	unrepresentable := []struct {
		name   string
		schema string
	}{
		{"additionalProperties true", `{"type":"object","properties":{"a":{"type":"string"}},"additionalProperties":true}`},
		{"additionalProperties schema", `{"type":"object","additionalProperties":{"type":"string"}}`},
		{"free-form object without properties", `{"type":"object"}`},
		{"not keyword", `{"type":"object","properties":{"a":{"not":{"type":"null"}}},"additionalProperties":false}`},
		{"anyOf with sibling type", `{"type":"object","properties":{"a":{"type":"string","anyOf":[{"const":"x"},{"const":"y"}]}},"additionalProperties":false}`},
		{"allOf with ref across two branches", `{"$defs":{"A":{"type":"object","properties":{"x":{"type":"string"}},"additionalProperties":false}},"allOf":[{"$ref":"#/$defs/A"},{"type":"object","properties":{"y":{"type":"string"}},"additionalProperties":false}]}`},
		{"complex enum", `{"type":"object","properties":{"a":{"enum":[{"k":"v"}]}},"additionalProperties":false}`},
		{"external ref", `{"$ref":"https://example.com/schema.json"}`},
		{"recursive ref", `{"$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}},"additionalProperties":false}},"$ref":"#/$defs/Node"}`},
	}
	for _, tc := range unrepresentable {
		t.Run("drops unrepresentable "+tc.name, func(t *testing.T) {
			req, err := openaichat.ParseRequest([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}],` +
				`"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":` + tc.schema + `}}}`))
			if err != nil {
				t.Fatal(err)
			}
			out, losses, err := EncodeRequestWith(req, EncodeOptions{StructuredOutputRules: true})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(out), `"output_config"`) {
				t.Errorf("body contains output_config for %s: %s", tc.name, out)
			}
			if got := lossList(losses); !reflect.DeepEqual(got, []string{"dropped response_format"}) {
				t.Errorf("losses = %q, want [dropped response_format]", got)
			}
		})
	}

	t.Run("same-format messages output_config stays byte-identical", func(t *testing.T) {
		const rawSchema = `{"type":"object","properties":{"a":{"type":"integer","minimum":1}}}`
		req, err := ParseRequest([]byte(`{"model":"claude-3-7-sonnet-20250219","max_tokens":128,"messages":[{"role":"user","content":"hi"}],` +
			`"output_config":{"format":{"type":"json_schema","schema":` + rawSchema + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if req.SourceFormat() != llm.FormatAnthropicMessages {
			t.Fatalf("SourceFormat = %q", req.SourceFormat())
		}
		out, losses, err := EncodeRequestWith(req, EncodeOptions{StructuredOutputRules: true})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte(rawSchema)) {
			t.Errorf("same-format schema was modified: %s", out)
		}
		if len(losses) != 0 {
			t.Errorf("losses = %q, want none", lossList(losses))
		}
	})
}

func TestEncodeStructuredOutputLeavesOtherUpstreamsAlone(t *testing.T) {
	const schema = `{"type":"object","properties":{"n":{"type":"integer","minimum":1}},"additionalProperties":true}`
	for _, model := range []string{"claude-opus-4-1", "claude-opus-5-5", "glm-5"} {
		req, err := openaichat.ParseRequest([]byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],` +
			`"response_format":{"type":"json_schema","json_schema":{"name":"r","schema":` + schema + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		out, losses, err := EncodeRequestWith(req, EncodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), schema) {
			t.Errorf("%s: schema changed: %s", model, out)
		}
		if len(losses) != 0 {
			t.Errorf("%s: losses = %+v", model, losses)
		}
	}
}

func TestStructuredSchemaRefChainIsLinear(t *testing.T) {
	const links = 60
	var defs strings.Builder
	for i := range links {
		fmt.Fprintf(&defs, `"d%d":{"anyOf":[{"$ref":"#/$defs/d%d"},{"$ref":"#/$defs/d%d"}]},`, i, i+1, i+1)
	}
	fmt.Fprintf(&defs, `"d%d":{"type":"string"}`, links)
	raw := json.RawMessage(`{"$defs":{` + defs.String() + `},"type":"object","properties":{"a":{"$ref":"#/$defs/d0"}},"additionalProperties":false}`)
	done := make(chan struct{})
	go func() {
		adaptStructuredSchema(raw)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("schema check did not finish")
	}
}

func TestStructuredSchemaNamesTheFormatItDrops(t *testing.T) {
	_, losses, ok := adaptStructuredSchema(json.RawMessage(`{"type":"object","properties":{"a":{"type":"string","format":"phone"}},"additionalProperties":false}`))
	if !ok || len(losses) != 1 || !strings.Contains(losses[0].Detail, `"phone"`) {
		t.Fatalf("ok = %v, losses = %+v", ok, losses)
	}
}
