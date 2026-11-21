package host

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestWebSearchDowngrade(t *testing.T) {
	maxUses := 3
	orig := &llm.Origin{
		Format: llm.FormatOpenAIResponses,
		Path:   "tools[0]",
		Raw:    json.RawMessage(`{"type":"web_search","max_uses":3}`),
	}
	req := llm.Request{
		Model: "claude-sonnet-4-6",
		Tools: []llm.Tool{
			{
				Type: llm.ToolWebSearch,
				Name: "web_search",
				WebSearch: &llm.WebSearchTool{
					MaxUses: &maxUses,
				},
				Origin: orig,
			},
			{
				Type: llm.ToolFunction,
				Name: "get_weather",
			},
		},
	}

	// Below minor 29: web_search becomes native with its Origin unchanged.
	got28 := downgradeWebSearch(28, req)
	if len(got28.Tools) != 2 {
		t.Fatalf("minor 28: len(Tools) = %d, want 2", len(got28.Tools))
	}
	if got28.Tools[0].Type != llm.ToolNative {
		t.Errorf("minor 28: Tools[0].Type = %q, want %q", got28.Tools[0].Type, llm.ToolNative)
	}
	if got28.Tools[0].WebSearch != nil {
		t.Errorf("minor 28: Tools[0].WebSearch = %+v, want nil", got28.Tools[0].WebSearch)
	}
	if !reflect.DeepEqual(got28.Tools[0].Origin, orig) {
		t.Errorf("minor 28: Tools[0].Origin = %+v, want %+v", got28.Tools[0].Origin, orig)
	}
	if got28.Tools[1].Name != "get_weather" {
		t.Errorf("minor 28: Tools[1] changed: %+v", got28.Tools[1])
	}
	// Original req must not be mutated.
	if req.Tools[0].Type != llm.ToolWebSearch {
		t.Errorf("original req was mutated: Tools[0].Type = %q", req.Tools[0].Type)
	}

	// At minor 29: unchanged.
	got29 := downgradeWebSearch(29, req)
	if !reflect.DeepEqual(got29, req) {
		t.Errorf("minor 29: req changed: got %+v, want %+v", got29, req)
	}
}

func TestWebSearchDowngradeChatOptions(t *testing.T) {
	raw := []byte(`{"model":"gpt-5-search-api","messages":[{"role":"user","content":"hi"}],"web_search_options":{"search_context_size":"low"},"seed":42}`)
	req, err := openaichat.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	origRawBefore := append([]byte(nil), req.Origin.Raw...)
	origPtr := req.Origin

	got := downgradeWebSearch(28, req)
	if len(got.Tools) != 0 {
		t.Fatalf("minor 28: len(Tools) = %d, want 0 (tools: %+v)", len(got.Tools), got.Tools)
	}
	// Must not mutate the caller's Origin.
	if got.Origin == origPtr {
		t.Errorf("minor 28: mutated caller Origin pointer")
	}
	if !bytes.Equal(origPtr.Raw, origRawBefore) {
		t.Errorf("minor 28: mutated caller Origin.Raw: got %s, want %s", origPtr.Raw, origRawBefore)
	}
	body, losses, err := openaichat.EncodeRequest(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(losses) != 0 {
		t.Errorf("unexpected losses: %+v", losses)
	}
	// Must match what base 45439babf produces: web_search_options at top level, no tools array.
	want := `{"messages":[{"role":"user","content":"hi"}],"model":"gpt-5-search-api","seed":42,"stream":false,"web_search_options":{"search_context_size":"low"}}`
	if string(body) != want {
		t.Errorf("encoded body:\ngot:  %s\nwant: %s", body, want)
	}
}

func TestAllowedToolsDowngradeExcludesWebSearch(t *testing.T) {
	req := llm.Request{
		Model: "gpt-5.4",
		Tools: []llm.Tool{
			{Type: llm.ToolWebSearch, Name: "web_search", WebSearch: &llm.WebSearchTool{}},
			{Type: llm.ToolFunction, Name: "get_weather"},
		},
		Options: llm.Options{
			ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceAuto, Allowed: []string{"web_search"}},
		},
	}
	if _, err := downgradeAllowedTools(25, req); err == nil {
		t.Fatalf("expected invalid_request when allowed_tools names only a ToolWebSearch, got nil")
	}
}
