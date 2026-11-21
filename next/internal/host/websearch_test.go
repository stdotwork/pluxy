package host

import (
	"encoding/json"
	"reflect"
	"testing"

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
