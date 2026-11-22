package clienttools_test

import (
	"testing"

	"github.com/stdotwork/pluxy/next/format/internal/clienttools"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestLowerKeepsWebSearchBesideSameNameFunction(t *testing.T) {
	req, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"hi",
		"tools":[
			{"type":"custom","name":"apply_patch","description":"apply a patch"},
			{"type":"web_search","name":"web_search"},
			{"type":"function","name":"web_search","parameters":{"type":"object","properties":{}}}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	lowered, losses := clienttools.Lower(req)
	if len(losses) != 0 {
		t.Fatalf("unexpected losses: %+v", losses)
	}
	var gotCustom, gotSearch, gotFn bool
	for _, tool := range lowered.Tools {
		switch {
		case tool.IsFunction() && tool.Name == "apply_patch":
			gotCustom = true
		case tool.Type == llm.ToolWebSearch && tool.Name == "web_search":
			gotSearch = true
		case tool.IsFunction() && tool.Name == "web_search":
			gotFn = true
		}
	}
	if !gotCustom || !gotSearch || !gotFn {
		t.Fatalf("tools after Lower = %+v, want apply_patch function, web_search ToolWebSearch, and web_search function", lowered.Tools)
	}
}
