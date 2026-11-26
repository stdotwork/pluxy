package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// shapes returns a body's contents as role:kind pairs: "user:text" for a text part ("user:empty"
// for the empty one the fix adds), "model:thought", "model:call", "model:response".
func shapes(t *testing.T, body []byte) string {
	t.Helper()
	var b struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				Text             *string         `json:"text"`
				Thought          bool            `json:"thought"`
				FunctionCall     json.RawMessage `json:"functionCall"`
				FunctionResponse json.RawMessage `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, body)
	}
	var out []string
	for _, c := range b.Contents {
		kind := "?"
		if len(c.Parts) > 0 {
			p := c.Parts[0]
			switch {
			case p.FunctionCall != nil:
				kind = "call"
			case p.FunctionResponse != nil:
				kind = "response"
			case p.Thought:
				kind = "thought"
			case p.Text != nil && *p.Text == "":
				kind = "empty"
			case p.Text != nil:
				kind = "text"
			}
		}
		out = append(out, c.Role+":"+kind)
	}
	return strings.Join(out, " ")
}

func TestFixTurns(t *testing.T) {
	const (
		user    = `{"role":"user","parts":[{"text":"hi"}]}`
		model   = `{"role":"model","parts":[{"text":"hello"}]}`
		thought = `{"role":"model","parts":[{"text":"hmm","thought":true}]}`
		call    = `{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}}]}`
		resp    = `{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}`
		modelFR = `{"role":"model","parts":[{"functionResponse":{"name":"f","response":{}}}]}`
		mediaFR = `{"role":"model","parts":[{"functionResponse":{"name":"f","response":{},"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`
		fileFR  = `{"role":"model","parts":[{"functionResponse":{"name":"f","response":{},"parts":[{"file_data":{"mime_type":"image/png","file_uri":"gs://b/a.png"}}]}}]}`
		userFR  = `{"role":"user","parts":[{"functionResponse":{"name":"f","response":{},"parts":[{"inlineData":{"mimeType":"image/png","data":"AAAA"}}]}}]}`
	)
	tests := []struct {
		name          string
		contents      []string
		vertex        bool
		stream        bool
		translated    bool
		want          string
		wantUnchanged bool
	}{
		{name: "user first and last is unchanged", contents: []string{user}, want: "user:text", wantUnchanged: true},
		{name: "model first gets a user turn", contents: []string{model, user}, want: "user:empty model:text user:text"},
		{name: "model last gets a user turn", contents: []string{user, model}, want: "user:text model:text user:empty"},
		{name: "a lone model turn gets both", contents: []string{model}, want: "user:empty model:text user:empty"},
		{name: "a last function call gets a user turn", contents: []string{user, call}, want: "user:text model:call user:empty"},
		{name: "a function response last is left for the model to answer", contents: []string{user, call, resp}, want: "user:text model:call user:response", wantUnchanged: true},
		{name: "a model-role function response without media is left", contents: []string{user, call, modelFR}, want: "user:text model:call model:response", wantUnchanged: true},
		{name: "a model-role function response with an image gets a user turn", contents: []string{user, call, mediaFR}, want: "user:text model:call model:response user:empty"},
		{name: "a model-role function response with a file gets a user turn", contents: []string{user, call, fileFR}, want: "user:text model:call model:response user:empty"},
		{name: "a user-role function response with an image is left", contents: []string{user, call, userFR}, want: "user:text model:call user:response", wantUnchanged: true},
		{name: "the Gemini API keeps trailing thoughts", contents: []string{user, model, thought, thought}, want: "user:text model:text model:thought model:thought user:empty"},
		{name: "Vertex drops trailing thought turns", contents: []string{user, model, thought, thought}, vertex: true, want: "user:text model:text user:empty"},
		{name: "Vertex drops a thought turn after the user's", contents: []string{user, thought}, vertex: true, want: "user:text"},
		{name: "Vertex keeps a thought turn that is not last", contents: []string{user, thought, user}, vertex: true, want: "user:text model:thought user:text", wantUnchanged: true},
		{name: "Vertex keeps a thought turn with a function call", contents: []string{user, `{"role":"model","parts":[{"thought":true,"functionCall":{"name":"f"}}]}`}, vertex: true, want: "user:text model:call user:empty"},
		{name: "Vertex keeps a turn with a thought and visible text", contents: []string{user, `{"role":"model","parts":[{"text":"a","thought":true},{"text":"b"}]}`}, vertex: true, want: "user:text model:thought user:empty"},
		{name: "Vertex drops a trailing signature-only turn of a translated request", contents: []string{user, `{"role":"model","parts":[{"text":"","thoughtSignature":"sig"}]}`}, vertex: true, translated: true, want: "user:text"},
		{name: "Vertex keeps a signature-only turn of a Gemini client's request", contents: []string{user, `{"role":"model","parts":[{"text":"","thoughtSignature":"sig"}]}`}, vertex: true, want: "user:text model:empty user:empty"},
		{name: "Vertex keeps a signature-only turn that has visible text with it", contents: []string{user, `{"role":"model","parts":[{"text":"","thoughtSignature":"sig"},{"text":"a"}]}`}, vertex: true, translated: true, want: "user:text model:empty user:empty"},
		{name: "one thought turn on Vertex, not streamed", contents: []string{thought}, vertex: true, want: "user:empty"},
		{name: "one thought turn on Vertex, streamed", contents: []string{thought}, vertex: true, stream: true, want: "user:empty model:thought user:empty"},
		{name: "one thought turn on the Gemini API, not streamed", contents: []string{thought}, want: "user:empty model:thought user:empty"},
		{name: "assistant is the model's role at the end", contents: []string{user, `{"role":"assistant","parts":[{"text":"a"}]}`}, want: "user:text assistant:text user:empty"},
		{name: "assistant is not a model first turn", contents: []string{`{"role":"assistant","parts":[{"text":"a"}]}`, user}, want: "assistant:text user:text", wantUnchanged: true},
		{name: "a turn with no role is left", contents: []string{`{"parts":[{"text":"a"}]}`}, want: ":text", wantUnchanged: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"contents":[` + strings.Join(tt.contents, ",") + `],"generationConfig":{"seed":9007199254740993},"note":"<a&b>"}`)
			got := fixTurns(body, tt.vertex, tt.stream, tt.translated)
			if s := shapes(t, got); s != tt.want {
				t.Fatalf("turns = %q, want %q\nbody: %s", s, tt.want, got)
			}
			if tt.wantUnchanged && !bytes.Equal(got, body) {
				t.Errorf("body was rewritten: %s", got)
			}
			// Every other field comes through as it was, large numbers and markup included.
			for _, keep := range []string{`"seed":9007199254740993`, `"note":"<a&b>"`} {
				if !bytes.Contains(got, []byte(keep)) {
					t.Errorf("body lost %s: %s", keep, got)
				}
			}
		})
	}
}

// The turn added is exactly the one today's core adds.
func TestFixTurnsAddedTurn(t *testing.T) {
	got := fixTurns([]byte(`{"contents":[{"role":"model","parts":[{"text":"a"}]}]}`), false, false, false)
	var b struct {
		Contents []json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(got, &b); err != nil || len(b.Contents) != 3 {
		t.Fatalf("err = %v, body = %s", err, got)
	}
	for _, i := range []int{0, 2} {
		if string(b.Contents[i]) != `{"role":"user","parts":[{"text":""}]}` {
			t.Errorf("contents[%d] = %s", i, b.Contents[i])
		}
	}
}

// A body that is not a request with contents is sent as it is.
func TestFixTurnsLeavesOtherBodies(t *testing.T) {
	for _, body := range []string{``, `not json`, `[]`, `{}`, `{"contents":[]}`, `{"contents":"x"}`, `{"contents":null}`, `{"generateContentRequest":{"contents":[{"role":"model","parts":[]}]}}`} {
		if got := fixTurns([]byte(body), true, true, true); string(got) != body {
			t.Errorf("fixTurns(%q) = %q", body, got)
		}
	}
}

// countTokens gets the leading turn only, and only on a top-level contents.
func TestFixLeadingTurn(t *testing.T) {
	got := fixLeadingTurn([]byte(`{"contents":[{"role":"model","parts":[{"text":"a"}]}]}`))
	if s := shapes(t, got); s != "user:empty model:text" {
		t.Errorf("turns = %q: %s", s, got)
	}
	for _, body := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"a"}]},{"role":"model","parts":[{"text":"b"}]}]}`,
		`{"generateContentRequest":{"contents":[{"role":"model","parts":[{"text":"a"}]}]}}`,
		`not json`,
	} {
		if got := fixLeadingTurn([]byte(body)); string(got) != body {
			t.Errorf("fixLeadingTurn(%q) = %q", body, got)
		}
	}
}
