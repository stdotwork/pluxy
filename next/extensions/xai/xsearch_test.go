package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/conformance"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/llm"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Capture the real generation request for both credentials without spending upstream tokens.
func TestXSearchRequests(t *testing.T) {
	for _, kind := range []string{protocol.CredentialAPIKey, protocol.CredentialOAuth} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name, body, want string
				inject           bool
			}{
				{"native preserved", `{"tools":[{"type":"x_search","allowed_x_handles":["example"]}]}`, `[{"type":"x_search","allowed_x_handles":["example"]}]`, false},
				{"default off", `{"unused":true}`, `null`, false},
				{"inject", `{"unused":true}`, `[{"type":"x_search"}]`, true},
				{"no duplicate", `{"tools":[{"type":"x_search"}]}`, `[{"type":"x_search"}]`, true},
				{"forced web search", `{"tools":[{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`, `[{"type":"web_search"}]`, true},
				{"forced image", `{"tools":[{"type":"image_generation"}],"tool_choice":"required"}`, `[{"type":"image_generation"}]`, true},
				{"forced web among functions", `{"tools":[{"type":"function","name":"f"},{"type":"web_search"}],"tool_choice":{"type":"web_search"}}`, `[{"type":"web_search"}]`, true},
				{"allowed hosted only", `{"tools":[{"type":"function","name":"f"},{"type":"web_search"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"web_search"}]}}`, `[{"type":"web_search"}]`, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var received []byte
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						received, _ = io.ReadAll(r.Body)
						w.WriteHeader(400)
						io.WriteString(w, `{"error":"fixture stop"}`)
					}))
					defer srv.Close()
					set, _ := json.Marshal(map[string]any{"base_url": srv.URL, "inject_x_search": tc.inject})
					cred := &protocol.Credential{Kind: kind, APIKey: "local-fixture", AccessToken: "local-fixture"}
					req, err := openairesponses.ParseRequest([]byte(`{"model":"grok-4.6","input":[],` + tc.body[1:]))
					if err != nil {
						t.Fatal(err)
					}
					if err = generate(t.Context(), sdk.Call{Account: protocol.Account{Settings: set, Credential: cred}, Request: req}, nil); err == nil {
						t.Fatal("refusing upstream unexpectedly succeeded")
					}
					var body map[string]json.RawMessage
					if err = json.Unmarshal(received, &body); err != nil {
						t.Fatal(err)
					}
					tools := body["tools"]
					if len(tools) == 0 {
						tools = []byte("null")
					}
					sameJSON(t, "tools", tools, tc.want)
				})
			}
		})
	}
}

func TestXSearchTraceIdentity(t *testing.T) {
	f := newSearchFilter([]byte(`{"tools":[{"type":"x_search"},{"type":"function","name":"x_keyword_search"}]}`))
	for _, tc := range []struct {
		item string
		drop bool
	}{
		{`{"type":"custom_tool_call","name":"x_keyword_search","call_id":"trace"}`, true},
		{`{"type":"function_call","name":"x_keyword_search","call_id":"client"}`, false},
		{`{"type":"function_call","name":"x_keyword_search","call_id":"xs_call1"}`, true},
		{`{"type":"custom_tool_call","namespace":"client","name":"x_keyword_search","call_id":"xs_call1"}`, false},
		{`{"type":"function_call","name":"x_thread_fetch","call_id":"internal"}`, true},
		{`{"type":"function_call","name":"other","call_id":"xs_call1"}`, false},
	} {
		m, _ := decodeObject([]byte(tc.item))
		if got := f.internal(m); got != tc.drop {
			t.Errorf("%s internal=%v, want %v", tc.item, got, tc.drop)
		}
	}
}

func TestXSearchAllowedTools(t *testing.T) {
	for _, body := range []string{
		`{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"f"}]}}`,
		`{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"f"},{"type":"x_search"}],"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"f"},{"type":"x_search"}]}}`,
	} {
		req, err := openairesponses.ParseRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := buildSearchBody(req, true)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools  []json.RawMessage `json:"tools"`
			Choice json.RawMessage   `json:"tool_choice"`
		}
		_ = json.Unmarshal(raw, &got)
		if len(got.Tools) != 2 {
			t.Errorf("tools = %s", raw)
		}
		sameJSON(t, "allowed tools", got.Choice, `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"f"},{"type":"x_search"}]}`)
	}
}

// Preserve client tools that reuse search names, citations, usage and opaque hosted items.
func TestXSearchJSONReply(t *testing.T) {
	manifest := conformance.BuildExtension(t, "pluxy-extension.json", ".")
	ext, err := host.Start(manifest, host.Options{Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Stop)
	for _, native := range []bool{false, true} {
		for _, inject := range []bool{false, true} {
			t.Run(fmt.Sprintf("native_%v_inject_%v", native, inject), func(t *testing.T) {
				const reply = `{"id":"resp_search","model":"grok-4.6","object":"response","status":"completed","output":[{"type":"custom_tool_call","id":"trace","call_id":"xs_call_1","name":"x_keyword_search","input":"internal query"},{"type":"function_call","id":"real","call_id":"client_1","name":"x_keyword_search","arguments":"{}"},{"type":"x_search_call","id":"search","status":"completed"},{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"Answer","annotations":[{"type":"url_citation","url":"https://x.com/example/status/1","title":"source","start_index":0,"end_index":6}]}]}],"usage":{"input_tokens":11,"output_tokens":7}}`
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					io.WriteString(w, reply)
				}))
				defer srv.Close()
				body := `{"model":"grok-4.6","input":[],"tools":[{"type":"function","name":"x_keyword_search"}`
				if native {
					body += `,{"type":"x_search"}`
				}
				body += `]}`
				req, err := openairesponses.ParseRequest([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				set, _ := json.Marshal(map[string]any{"base_url": srv.URL, "inject_x_search": inject})
				s := ext.Generate(t.Context(), protocol.Account{ID: "xai", Settings: set, Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: "local-fixture"}}, req)
				defer s.Close()
				agg := openairesponses.NewAggregator(req)
				for {
					ev := s.Next()
					if ev.Type == llm.EventError {
						t.Fatal(ev.Error)
					}
					agg.Add(ev)
					if ev.Terminal() {
						break
					}
				}
				got, err := agg.Response()
				if err != nil {
					t.Fatal(err)
				}
				var expected map[string]any
				_ = json.Unmarshal([]byte(reply), &expected)
				if native || inject {
					expected["output"] = expected["output"].([]any)[1:]
				}
				want, _ := json.Marshal(expected)
				sameJSON(t, "client reply", got, string(want))
			})
		}
	}
}

func TestXSearchToolLimit(t *testing.T) {
	body := `{"model":"grok-4.6","input":[],"tools":[` + strings.Repeat(`{"type":"function","name":"f"},`, maxTools-1) + `{"type":"function","name":"last"}]}`
	req, err := openairesponses.ParseRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := buildSearchBody(req, true)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Tools []json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(raw, &m)
	if len(m.Tools) != maxTools || strings.Contains(string(raw), `"type":"x_search"`) {
		t.Errorf("tool limit not kept: %s", raw)
	}
}
