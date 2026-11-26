package main

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

// Vertex's grounding page allows several tools in one request only when all are search tools, and
// Gemini 2.0 refuses googleSearch with codeExecution; elsewhere the Gemini API takes the pair.
func TestCodeExecutionBesideSearch(t *testing.T) {
	const searchFirst = `[{"type":"web_search"},{"type":"code_interpreter","container":{"type":"auto"}}]`
	const codeFirst = `[{"type":"code_interpreter","container":{"type":"auto"}},{"type":"web_search"}]`
	vertex := settings{Vertex: &vertexSettings{Project: "p", Location: "us-central1"}}
	cases := []struct {
		name     string
		set      settings
		model    string
		tools    string
		wantBoth bool   // both tools are sent; otherwise search alone and the code tool is reported dropped
		dropped  string // path of the code tool, when dropped
	}{
		{"vertex", vertex, "gemini-2.5-flash", searchFirst, false, "tools[1]"},
		{"vertex, code tool first", vertex, "gemini-2.5-flash", codeFirst, false, "tools[0]"},
		{"vertex, gemini 4", vertex, "gemini-4-hydrogen", searchFirst, false, "tools[1]"},
		{"gemini api, 2.0", settings{BaseURL: defaultBaseURL}, "gemini-2.0-flash", searchFirst, false, "tools[1]"},
		{"gemini api, 2.0, code tool first", settings{BaseURL: defaultBaseURL}, "gemini-2.0-flash", codeFirst, false, "tools[0]"},
		{"gemini api, 2.5", settings{BaseURL: defaultBaseURL}, "gemini-2.5-flash", searchFirst, true, ""},
		{"gemini api, 2.5, code tool first", settings{BaseURL: defaultBaseURL}, "gemini-2.5-flash", codeFirst, true, ""},
		{"gemini api, gemini 4", settings{BaseURL: defaultBaseURL}, "gemini-4-hydrogen", searchFirst, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(`{"model":"` + tc.model + `","input":"search and compute","tools":` + tc.tools + `}`))
			if err != nil {
				t.Fatal(err)
			}
			req = roundTripReq(t, req)
			body, tr, err := buildBodyOn(req, tc.set)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			var search, code int
			for _, tool := range got.Tools {
				if tool["googleSearch"] != nil {
					search++
				}
				if tool["codeExecution"] != nil {
					code++
				}
			}
			if search != 1 {
				t.Errorf("googleSearch sent %d times, want once (body: %s)", search, body)
			}
			if tc.wantBoth {
				if code != 1 {
					t.Errorf("codeExecution sent %d times, want once (body: %s)", code, body)
				}
				if tr.Dropped() {
					t.Errorf("unexpected dropped losses: %+v", tr.Losses)
				}
				return
			}
			if code != 0 {
				t.Errorf("codeExecution sent beside googleSearch (body: %s)", body)
			}
			if !slices.Contains(tr.DroppedPaths(), tc.dropped) {
				t.Errorf("dropped = %v, want %q reported (losses %+v)", tr.DroppedPaths(), tc.dropped, tr.Losses)
			}
			reject := req
			reject.DropPolicy = llm.DropReject
			if _, _, err := buildBodyOn(reject, tc.set); err == nil {
				t.Errorf("drop_policy=reject: want a refusal for the dropped tool, got none")
			}
		})
	}
}

// Every translated code tool is judged, not only the last: two of them beside a search are both
// dropped where the pair is refused, and both report their own losses where it is kept.
func TestTwoCodeExecutionToolsBesideSearch(t *testing.T) {
	const tools = `[{"type":"code_interpreter","container":"cntr_a"},{"type":"web_search"},{"type":"code_interpreter","container":"cntr_b"}]`
	cases := []struct {
		name  string
		set   settings
		model string
		drop  bool
	}{
		{"vertex", settings{Vertex: &vertexSettings{Project: "p", Location: "us-central1"}}, "gemini-2.5-flash", true},
		{"gemini api, 2.0", settings{BaseURL: defaultBaseURL}, "gemini-2.0-flash", true},
		{"gemini api, 2.5", settings{BaseURL: defaultBaseURL}, "gemini-2.5-flash", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := openairesponses.ParseRequest([]byte(`{"model":"` + tc.model + `","input":"x","tools":` + tools + `}`))
			if err != nil {
				t.Fatal(err)
			}
			req = roundTripReq(t, req)
			body, tr, err := buildBodyOn(req, tc.set)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Tools []map[string]json.RawMessage `json:"tools"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			var search, code int
			for _, tool := range got.Tools {
				if tool["googleSearch"] != nil {
					search++
				}
				if tool["codeExecution"] != nil {
					code++
				}
			}
			if search != 1 {
				t.Errorf("googleSearch sent %d times, want once (body: %s)", search, body)
			}
			paths := tr.DroppedPaths()
			if tc.drop {
				if code != 0 {
					t.Errorf("codeExecution sent %d times beside googleSearch, want none (body: %s)", code, body)
				}
				for _, want := range []string{"tools[0]", "tools[2]"} {
					if !slices.Contains(paths, want) {
						t.Errorf("dropped = %v, want %q reported", paths, want)
					}
				}
				return
			}
			if code != 2 {
				t.Errorf("codeExecution sent %d times, want 2 (body: %s)", code, body)
			}
			for _, want := range []string{"tools[0].container", "tools[2].container"} {
				if !slices.Contains(paths, want) {
					t.Errorf("dropped = %v, want %q reported for the kept tool", paths, want)
				}
			}
		})
	}
}
