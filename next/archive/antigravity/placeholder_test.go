package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/archive/antigravity/agtest"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
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
// boolean) where there are some and none is required. The placeholders expected are today's core's
// (corpus case tool-schema-forms-claude records the nested object), and so is the rest of each
// schema, allOf merged as today's core merges it (the allOf cases are its output through its
// executor's tool cleaning). One difference on purpose: a function's response schema gets no
// placeholder here (today's core adds one to a declaration's response and responseJsonSchema), as
// it describes what the client returns, not what the model writes, and nothing would strip it. A
// site is left out where a client schema at the same path, a union branch (also one Claude's
// cleaning cuts), a tuple item or a function declared again under the same name, declares the
// placeholder's name as a property or in required, so a strip never takes the client's key; today's
// core keeps the placeholder in the model's calls there too.
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
		{"a sibling union branch declares _", false, true,
			`{"type":"object","properties":{"u":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"_":{"type":"string"}}}]}},"required":["u"]}`,
			`{"type":"object","properties":{"u":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]},{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}]}},"required":["u"]}`,
			nil},
		{"a sibling union branch declares reason", false, true,
			`{"type":"object","properties":{"u":{"oneOf":[{"type":"object"},{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"]}]}},"required":["u"]}`,
			`{"type":"object","properties":{"u":{"oneOf":[{"type":"object","properties":{"reason":` + reason + `},"required":["reason"]},{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"]}]}},"required":["u"]}`,
			nil},
		{"a tuple item declares _", true, true,
			`{"type":"object","properties":{"t":{"type":"array","items":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}]}},"required":["t"]}`,
			`{"type":"object","properties":{"t":{"type":"array","items":[{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]},{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}]}},"required":["t"]}`,
			nil},
		{"an allOf branch requires a property", true, true,
			`{"type":"object","allOf":[{"properties":{"a":{"type":"string"}},"required":["a"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
			nil},
		{"allOf branches require nothing", true, true,
			`{"type":"object","allOf":[{"properties":{"a":{"type":"string"}}},{"properties":{"b":{"type":"string"}}}]}`,
			`{"type":"object","properties":{"_":{"type":"boolean"},"a":{"type":"string"},"b":{"type":"string"}},"required":["_"]}`,
			[]placeholderSite{{name: "_"}}},
		{"an allOf branch declares _", true, true,
			`{"type":"object","allOf":[{"properties":{"_":{"type":"string"}}}]}`,
			`{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}`,
			nil},
		{"properties and no type", true, true,
			`{"properties":{"a":{"type":"string"}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}`,
			[]placeholderSite{{name: "_"}}},
		{"empty properties and no type", true, true,
			`{"properties":{}}`,
			`{"type":"object","properties":{"reason":` + reason + `},"required":["reason"]}`,
			[]placeholderSite{{name: "reason"}}},
		{"properties and no type, with a required property", true, true,
			`{"properties":{"a":{"type":"string"}},"required":["a"]}`,
			`{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}`,
			nil},
		{"no type, properties only from allOf", true, true,
			`{"allOf":[{"type":"object","properties":{"a":{"type":"string"}}}]}`,
			`{"type":"object","properties":{"_":{"type":"boolean"},"a":{"type":"string"}},"required":["_"]}`,
			[]placeholderSite{{name: "_"}}},
		{"Claude: a cut union branch declares _", true, true,
			`{"type":"object","properties":{"u":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}]}},"required":["u"]}`,
			`{"type":"object","properties":{"u":{"type":"object","description":"Accepts: object | object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}},"required":["u"]}`,
			nil},
		{"Claude: a cut union branch declares reason", true, true,
			`{"type":"object","properties":{"u":{"oneOf":[{"type":"object"},{"type":"object","properties":{"reason":{"type":"string"}},"required":["reason"]}]}},"required":["u"]}`,
			`{"type":"object","properties":{"u":{"type":"object","description":"Accepts: object | object","properties":{"reason":` + reason + `},"required":["reason"]}},"required":["u"]}`,
			nil},
		{"Claude: a cut union branch declares _ through a $ref", true, true,
			`{"type":"object","properties":{"u":{"anyOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}},"required":["u"],
				"$defs":{"A":{"type":"object","properties":{"a":{"type":"string"}}},"B":{"type":"object","properties":{"_":{"type":"string"}},"required":["_"]}}}`,
			`{"type":"object","properties":{"u":{"type":"object","description":"Accepts: object | object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}},"required":["u"]}`,
			nil},
		{"a name only in required: reason", true, true,
			`{"type":"object","properties":{"x":{"type":"object","required":["reason"]}},"required":["x"]}`,
			`{"type":"object","properties":{"x":{"type":"object","properties":{"reason":` + reason + `},"required":["reason"]}},"required":["x"]}`,
			nil},
		{"a name only in required: _", true, true,
			`{"type":"object","properties":{"x":{"properties":{"a":{"type":"string"}},"required":["_"]}},"required":["x"]}`,
			`{"type":"object","properties":{"x":{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}},"required":["x"]}`,
			nil},
		{"other models: an untyped schema with properties gets its type", false, false,
			`{"properties":{"a":{"type":"string"},"n":{"properties":{"b":{"type":"string"}}}}}`,
			`{"type":"object","properties":{"a":{"type":"string"},"n":{"type":"object","properties":{"b":{"type":"string"}}}}}`,
			nil},
		{"a union object gets none (anyOf)", false, true,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"anyOf":[{"required":["a"]},{"required":["b"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"anyOf":[{"required":["a"]},{"required":["b"]}]}`,
			nil},
		{"a union object gets none (oneOf)", false, true,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`,
			`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`,
			nil},
		{"additionalProperties is gone before the placeholder", true, true,
			`{"type":"object","additionalProperties":{"type":"object"}}`,
			`{"type":"object","properties":{"reason":` + reason + `},"required":["reason"]}`,
			[]placeholderSite{{name: "reason"}}},
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
	// An untyped response schema with properties still gets its type, as in today's core.
	untyped := `[{"functionDeclarations":[{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},"response":{"properties":{"r":{"type":"string"}}}}]}]`
	typed := `[{"functionDeclarations":[{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]},"response":{"type":"object","properties":{"r":{"type":"string"}}}}]}]`
	out, _ = cleanTools(json.RawMessage(untyped), true, true)
	if !sameJSON(t, string(out), typed) {
		t.Errorf("cleaned tools %s, want %s", out, typed)
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

// A function declared again under one name, with a schema that declares the placeholder's name,
// keeps the placeholder the first declaration got in the model's calls: stripping would take the
// second declaration's key. Another function's placeholder is stripped as usual.
func TestPlaceholdersSameNameDeclarations(t *testing.T) {
	in := `[{"functionDeclarations":[
		{"name":"f","parametersJsonSchema":{"type":"object","properties":{"a":{"type":"string"}}}},
		{"name":"f","parametersJsonSchema":{"type":"object","properties":{"_":{"type":"string"}}}},
		{"name":"g","parametersJsonSchema":{"type":"object","properties":{"a":{"type":"string"}}}}]}]`
	// The same two declarations, the declaring one first.
	reversed := `[{"functionDeclarations":[
		{"name":"f","parametersJsonSchema":{"type":"object","properties":{"_":{"type":"string"}}}},
		{"name":"f","parametersJsonSchema":{"type":"object","properties":{"a":{"type":"string"}}}}]}]`
	for name, tools := range map[string]string{"declaring one second": in, "declaring one first": reversed} {
		_, added := cleanTools(json.RawMessage(tools), true, true)
		if len(added["f"]) != 0 {
			t.Errorf("%s: f's sites %+v, want none: one declaration declares _", name, added["f"])
		}
		if want := []placeholderSite{{name: "_"}}; name == "declaring one second" && !reflect.DeepEqual(added["g"], want) {
			t.Errorf("g's sites %+v, want %+v", added["g"], want)
		}
	}
}

// What the client sees of a call the model made: the arguments, and the raw chunks.
type callReply struct {
	args, raw string
	path      string          // the Cloud Code method the request went to
	upstream  json.RawMessage // the parameters of function f in the request Cloud Code got
}

// generateCall sends a Gemini-format request for model, with function f's parameters, through
// generateTo to the fake Cloud Code, which answers with a call to f with args, streamed or in one
// reply as the model's path asks.
func generateCall(t *testing.T, model, parameters, args string) callReply {
	t.Helper()
	srv := agtest.New(t)
	var sent []byte
	var path string
	srv.Generate = func(w http.ResponseWriter, r *http.Request, req agtest.Request) {
		sent = req.Body
		path = r.URL.Path
		chunk := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":` + args + `}}]},` +
			`"finishReason":"STOP","index":0}],"modelVersion":"agtest","responseId":"agtest"}}`
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+chunk+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chunk)
	}
	access, refresh := srv.Grant()
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"f","parametersJsonSchema":` + parameters + `}]}]}`
	call := sdk.Call{
		Account: protocol.Account{ID: "a", Settings: srv.Settings(), Credential: signedIn(access, refresh)},
		Request: llm.Request{Model: model, Native: &llm.Native{Format: llm.FormatGeminiGenerate, Body: json.RawMessage(body)}},
	}
	var c collect
	if err := generateTo(context.Background(), call, &c); err != nil {
		t.Fatal(err)
	}
	var reply callReply
	for _, ev := range c.events {
		if ev.Type == llm.EventToolInputDelta {
			reply.args += ev.Delta
		}
		reply.raw += string(ev.Raw)
	}
	var env struct {
		Request struct {
			Tools []struct {
				Decls []struct {
					Parameters json.RawMessage `json:"parameters"`
				} `json:"functionDeclarations"`
			} `json:"tools"`
		} `json:"request"`
	}
	if err := json.Unmarshal(sent, &env); err != nil || len(env.Request.Tools) != 1 || len(env.Request.Tools[0].Decls) != 1 {
		t.Fatalf("Cloud Code got %s (%v)", sent, err)
	}
	reply.upstream = env.Request.Tools[0].Decls[0].Parameters
	reply.path = path
	return reply
}

// A placeholder the request carried never reaches the client in a call the model made, on a
// streamed reply (Claude) and on a single reply (Gemini 3.1 Pro), and the model's other
// arguments are as it wrote them. A model that gets no placeholder keeps every key it sent.
func TestGenerateStripsPlaceholders(t *testing.T) {
	const optional = `{"type":"object","properties":{"a":{"type":"string"}}}`
	for _, c := range []struct {
		name, model, parameters, args string
		sent                          string // the parameters Cloud Code must get
		wantArgs                      string
		path                          string // the Cloud Code method the model's path asks for
		stripped                      bool
	}{
		{"Claude, streamed", "claude-sonnet-5-5", optional, `{"_":true,"a":"x"}`,
			`{"type":"object","properties":{"a":{"type":"string"},"_":{"type":"boolean"}},"required":["_"]}`, `{"a":"x"}`, "/v1internal:streamGenerateContent", true},
		{"Gemini 3.1 Pro, one reply", "gemini-3.1-pro-high", `{"type":"object"}`, `{"reason":"why"}`,
			`{"type":"object","properties":{"reason":{"type":"string","description":"Brief explanation of why you are calling this tool"}},"required":["reason"]}`, `{}`, "/v1internal:generateContent", true},
		{"a model outside the group", "gemini-3-flash", optional, `{"_":true,"a":"x"}`, optional, `{"_":true,"a":"x"}`, "/v1internal:generateContent", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := generateCall(t, c.model, c.parameters, c.args)
			if !sameJSON(t, string(got.upstream), c.sent) {
				t.Errorf("Cloud Code got parameters\n %s\nwant\n %s", got.upstream, c.sent)
			}
			if got.path != c.path {
				t.Errorf("the request went to %s, want %s", got.path, c.path)
			}
			if got.args != c.wantArgs {
				t.Errorf("the client's call has arguments %s, want %s", got.args, c.wantArgs)
			}
			if c.stripped && (strings.Contains(got.raw, `"_"`) || strings.Contains(got.raw, `"reason"`)) {
				t.Errorf("the raw reply still has the placeholder: %s", got.raw)
			}
		})
	}
}
