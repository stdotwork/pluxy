package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// Messages refuses a request whose tools share a name. A hosted tool translated from a Responses
// request is dropped, with a reported loss, when a function or another hosted tool has its name.
func TestHostedToolNameTakenIsDropped(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantTools []string // "name/type" of each tool sent, in order; a function has no type
		wantPath  string   // the dropped tool
	}{
		{
			name:      "function named code_execution",
			raw:       `{"model":"claude-sonnet-4-6","input":"run","tools":[{"type":"function","name":"code_execution","parameters":{"type":"object"}},{"type":"code_interpreter","container":{"type":"auto"}}]}`,
			wantTools: []string{"code_execution/"},
			wantPath:  "tools[1]",
		},
		{
			name:      "function declared after the hosted tool",
			raw:       `{"model":"claude-sonnet-4-6","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}},{"type":"function","name":"code_execution","parameters":{"type":"object"}}]}`,
			wantTools: []string{"code_execution/"},
			wantPath:  "tools[0]",
		},
		{
			name:      "two code interpreters",
			raw:       `{"model":"claude-sonnet-4-6","input":"run","tools":[{"type":"code_interpreter","container":{"type":"auto"}},{"type":"code_interpreter","container":{"type":"auto"}}]}`,
			wantTools: []string{"code_execution/code_execution_20250825"},
			wantPath:  "tools[1]",
		},
		{
			name:      "function named web_search",
			raw:       `{"model":"claude-sonnet-4-6","input":"look","tools":[{"type":"function","name":"web_search","parameters":{"type":"object"}},{"type":"web_search"}]}`,
			wantTools: []string{"web_search/"},
			wantPath:  "tools[1]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			req = roundTripRequest(t, req)
			body, tr, err := buildBodyWith(req, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, req, defaultBaseURL))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]any `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			seen := map[any]bool{}
			var sent []string
			for _, tool := range got.Tools {
				if seen[tool["name"]] {
					t.Errorf("tool name %v sent twice: %s", tool["name"], body)
				}
				seen[tool["name"]] = true
				typ, _ := tool["type"].(string)
				sent = append(sent, tool["name"].(string)+"/"+typ)
			}
			if !slices.Equal(sent, tc.wantTools) {
				t.Fatalf("tools sent = %v, want %v (body: %s)", sent, tc.wantTools, body)
			}
			if !slices.Contains(tr.DroppedPaths(), tc.wantPath) {
				t.Errorf("dropped = %v, want %q reported (losses %+v)", tr.DroppedPaths(), tc.wantPath, tr.Losses)
			}
			reqReject := req
			reqReject.DropPolicy = llm.DropReject
			if _, _, err := buildBodyWith(reqReject, cacheTTL5m, serviceOptions(anthropicmessages.EncodeOptions{}, reqReject, defaultBaseURL)); err == nil {
				t.Errorf("drop_policy=reject: want a refusal for the dropped tool, got none")
			}
		})
	}
}
