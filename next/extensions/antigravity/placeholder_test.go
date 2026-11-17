package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/llm"
)

// sameJSON reports whether two JSON texts hold the same value.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad want: %s", want)
	}
	return reflect.DeepEqual(g, w)
}

// declaration wraps a parameters schema in the tools array of one function f.
func declaration(params string) string {
	return `[{"functionDeclarations":[{"name":"f","parametersJsonSchema":` + params + `}]}]`
}

// Claude's VALIDATED mode wants a required property in every object, so today's core adds one to
// each object that requires nothing: "reason" (a string) where there are no properties, "_" (a
// boolean) where there are some and none is required. The expected schemas are today's core's
// (corpus case tool-schema-forms-claude records the nested object).
func TestPlaceholdersAdded(t *testing.T) {
	const reason = `{"type":"string","description":"Brief explanation of why you are calling this tool"}`
	any1 := placeholderStep{any: true}
	for _, c := range []struct {
		name          string
		claude, group bool
		params        string
		want          string // the cleaned parameters
		sites         []placeholderSite
	}{
		{"a nested object that requires nothing", true, true,
			`{"type":"object","properties":{"unit":{"type":"string"},"target":{"anyOf":[
				{"type":"object","properties":{"city":{"type":"string"}}},
				{"type":"object","properties":{"lat":{"type":"number"},"lon":{"type":"number"}}}]}},"required":["unit"]}`,
			`{"type":"object","properties":{"unit":{"type":"string"},"target":{"type":"object","description":"Accepts: object | object",
				"properties":{"city":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}},"required":["unit"]}`,
			[]placeholderSite{{path: []placeholderStep{{key: "target"}}, name: "_"}}},
		{"a function with no parameters", true, true,
			`{"type":"object"}`,
			`{"type":"object","properties":{"reason":` + reason + `},"required":["reason"]}`,
			[]placeholderSite{{name: "reason"}}},
		{"empty properties", true, true,
			`{"type":"object","properties":{}}`,
			`{"type":"object","properties":{"reason":` + reason + `},"required":["reason"]}`,
			[]placeholderSite{{name: "reason"}}},
		{"parameters that are all optional", true, true,
			`{"type":"object","properties":{"a":{"type":"string"}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}`,
			[]placeholderSite{{name: "_"}}},
		{"array items", true, true,
			`{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"}}}}},"required":["rows"]}`,
			`{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"x":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}}},"required":["rows"]}`,
			[]placeholderSite{{path: []placeholderStep{{key: "rows"}, any1}, name: "_"}}},
		{"a required name that is no property", true, true,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["ghost"]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}`,
			[]placeholderSite{{name: "_"}}},
		{"a declared _ stays the client's", true, true,
			`{"type":"object","properties":{"_":{"type":"string"}}}`,
			`{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}`,
			nil},
		{"an object with a required property", true, true,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"object","properties":{"c":{"type":"string"}},"required":["c"]}},"required":["a"]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"object","properties":{"c":{"type":"string"}},"required":["c"]}},"required":["a"]}`,
			nil},
		{"Gemini 3 Pro keeps unions and gets placeholders", false, true,
			`{"type":"object","properties":{"u":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"string"}]}},"required":["u"]}`,
			`{"type":"object","properties":{"u":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]},{"type":"string"}]}},"required":["u"]}`,
			[]placeholderSite{{path: []placeholderStep{{key: "u"}}, name: "_"}}},
		{"other models get none", false, false,
			`{"type":"object","properties":{"a":{"type":"string"}}}`,
			`{"type":"object","properties":{"a":{"type":"string"}}}`,
			nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, added := cleanTools(json.RawMessage(declaration(c.params)), c.claude, c.group)
			var tools []struct {
				Decls []struct {
					Parameters json.RawMessage `json:"parameters"`
				} `json:"functionDeclarations"`
			}
			if err := json.Unmarshal(out, &tools); err != nil || len(tools) != 1 || len(tools[0].Decls) != 1 {
				t.Fatalf("cleaned tools: %s (%v)", out, err)
			}
			if got := string(tools[0].Decls[0].Parameters); !sameJSON(t, got, c.want) {
				t.Errorf("parameters\n %s\nwant\n %s", got, c.want)
			}
			if !reflect.DeepEqual(added["f"], c.sites) {
				t.Errorf("sites %+v, want %+v", added["f"], c.sites)
			}
		})
	}
}

// A function's response schema describes what the client returns, not what the model writes: it
// gets no placeholder.
func TestPlaceholdersLeaveResponseSchemas(t *testing.T) {
	in := `[{"functionDeclarations":[{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},"response":{"type":"object"}}]}]`
	out, _ := cleanTools(json.RawMessage(in), true, true)
	if !sameJSON(t, string(out), in) {
		t.Errorf("cleaned tools %s, want %s", out, in)
	}
}

// The model's calls reach the client without the placeholders, whatever its format, and nothing
// else in the reply changes.
func TestPlaceholdersStripped(t *testing.T) {
	added := placeholders{"f": {
		{name: "_"},
		{path: []placeholderStep{{key: "target"}}, name: "reason"},
		{path: []placeholderStep{{key: "rows"}, {any: true}}, name: "_"},
	}}
	chunk := `{"candidates":[{"content":{"role":"model","parts":[{"text":"x"},` +
		`{"functionCall":{"name":"f","args":{"_":true,"a":"<b> & 1e3","big":12345678901234567890,"target":{"reason":"why","city":"Oslo"},"rows":[{"_":true,"x":1},{"x":2}]}},"thoughtSignature":"sig"},` +
		`{"functionCall":{"name":"other","args":{"_":true}}}]},"finishReason":"STOP"}],"modelVersion":"m"}`
	got := string(added.strip([]byte(chunk)))
	want := `{"candidates":[{"content":{"role":"model","parts":[{"text":"x"},` +
		`{"functionCall":{"name":"f","args":{"a":"<b> & 1e3","big":12345678901234567890,"target":{"city":"Oslo"},"rows":[{"x":1},{"x":2}]}},"thoughtSignature":"sig"},` +
		`{"functionCall":{"name":"other","args":{"_":true}}}]},"finishReason":"STOP"}],"modelVersion":"m"}`
	if !sameJSON(t, got, want) {
		t.Errorf("stripped chunk\n %s\nwant\n %s", got, want)
	}
	if !strings.Contains(got, `"big":12345678901234567890`) || !strings.Contains(got, `"<b> & 1e3"`) {
		t.Errorf("the arguments' numbers or text changed: %s", got)
	}
	// A chunk with nothing to remove comes back byte for byte.
	for _, plain := range []string{
		`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{"a":1}}}]}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f"}}]}}]}`,
		`not json "functionCall"`,
	} {
		if got := string(added.strip([]byte(plain))); got != plain {
			t.Errorf("changed %s into %s", plain, got)
		}
	}
	var none placeholders
	if got := string(none.strip([]byte(chunk))); got != chunk {
		t.Errorf("a request with no placeholders changed the reply: %s", got)
	}
}

type collect struct{ events []llm.Event }

func (c *collect) Send(ev llm.Event) error { c.events = append(c.events, ev); return nil }

// A streamed call has its placeholder removed in the model events and in the raw chunk a Gemini
// client gets.
func TestStreamStripsPlaceholders(t *testing.T) {
	added := placeholders{"f": {{name: "_"}}}
	body := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{"_":true,"a":1}}}]},"finishReason":"STOP"}]}}` + "\n\n"
	var c collect
	if err := relayStream(context.Background(), strings.NewReader(body), &c, added); err != nil {
		t.Fatal(err)
	}
	var args string
	var raw string
	for _, ev := range c.events {
		if ev.Type == llm.EventToolInputDelta {
			args += ev.Delta
		}
		raw += string(ev.Raw)
	}
	if args != `{"a":1}` {
		t.Errorf("the call's arguments are %s, want {\"a\":1}", args)
	}
	if strings.Contains(raw, `"_"`) || !strings.Contains(raw, `"a":1`) {
		t.Errorf("the raw chunk is %s", raw)
	}
}
