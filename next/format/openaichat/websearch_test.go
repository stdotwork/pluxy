package openaichat_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
)

func TestWebSearchSettingsAndUnmodelledFieldLosses(t *testing.T) {
	t.Run("chat_to_responses_carries_search_context_size_and_reports_extra", func(t *testing.T) {
		req, err := openaichat.ParseRequest([]byte(`{
			"model":"gpt-5.4",
			"messages":[{"role":"user","content":"hi"}],
			"web_search_options":{"search_context_size":"low","custom_opt":true}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openairesponses.EncodeRequestWith(req, openairesponses.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Tools) != 1 || got.Tools[0]["search_context_size"] != "low" {
			t.Fatalf("tools = %v, want search_context_size=low (body: %s)", got.Tools, body)
		}
		dropped := (&llm.Translation{Losses: losses}).DroppedPaths()
		if !slices.Equal(dropped, []string{"web_search_options.custom_opt"}) {
			t.Errorf("dropped = %v, want [web_search_options.custom_opt] (losses: %+v)", dropped, losses)
		}
	})

	t.Run("responses_to_chat_carries_search_context_size_and_reports_extra", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{
			"model":"gpt-5-search-api",
			"input":"hi",
			"tools":[{"type":"web_search","search_context_size":"high","extra_field":"x"}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := openaichat.EncodeRequestWith(req, openaichat.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			WebSearchOptions map[string]any `json:"web_search_options"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		if got.WebSearchOptions["search_context_size"] != "high" {
			t.Fatalf("web_search_options = %v, want search_context_size=high", got.WebSearchOptions)
		}
		dropped := (&llm.Translation{Losses: losses}).DroppedPaths()
		if !slices.Equal(dropped, []string{"tools[0].extra_field"}) {
			t.Errorf("dropped = %v, want [tools[0].extra_field] (losses: %+v)", dropped, losses)
		}
	})

	t.Run("chat_search_context_size_dropped_on_claude_and_gemini", func(t *testing.T) {
		req, err := openaichat.ParseRequest([]byte(`{
			"model":"claude-sonnet-4-6",
			"messages":[{"role":"user","content":"hi"}],
			"web_search_options":{"search_context_size":"low"}
		}`))
		if err != nil {
			t.Fatal(err)
		}
		_, claudeLosses, err := anthropicmessages.EncodeRequestWith(req, anthropicmessages.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := (&llm.Translation{Losses: claudeLosses}).DroppedPaths(); !slices.Equal(got, []string{"web_search_options.search_context_size"}) {
			t.Errorf("claude dropped = %v, want [web_search_options.search_context_size]", got)
		}

		reqG := req
		reqG.Model = "gemini-2.5-flash"
		_, gemLosses, err := geminigenerate.EncodeRequestWith(reqG, geminigenerate.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := (&llm.Translation{Losses: gemLosses}).DroppedPaths(); !slices.Equal(got, []string{"web_search_options.search_context_size"}) {
			t.Errorf("gemini dropped = %v, want [web_search_options.search_context_size]", got)
		}
	})

	t.Run("messages_cache_control_dropped_on_other_targets", func(t *testing.T) {
		req, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"gpt-5-search-api",
			"max_tokens":1024,
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"web_search_20250305","name":"web_search","cache_control":{"type":"ephemeral"}}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		_, respLosses, err := openairesponses.EncodeRequestWith(req, openairesponses.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := (&llm.Translation{Losses: respLosses}).DroppedPaths(); !slices.Equal(got, []string{"tools[0].cache_control"}) {
			t.Errorf("responses dropped = %v, want [tools[0].cache_control]", got)
		}

		_, chatLosses, err := openaichat.EncodeRequestWith(req, openaichat.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := (&llm.Translation{Losses: chatLosses}).DroppedPaths(); !slices.Equal(got, []string{"tools[0].cache_control"}) {
			t.Errorf("chat dropped = %v, want [tools[0].cache_control]", got)
		}

		reqG := req
		reqG.Model = "gemini-2.5-flash"
		_, gemLosses, err := geminigenerate.EncodeRequestWith(reqG, geminigenerate.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := (&llm.Translation{Losses: gemLosses}).DroppedPaths(); !slices.Equal(got, []string{"tools[0].cache_control"}) {
			t.Errorf("gemini dropped = %v, want [tools[0].cache_control]", got)
		}
	})

	t.Run("responses_to_claude_keeps_exact_output_and_reports_unmodelled_fields", func(t *testing.T) {
		req, err := openairesponses.ParseRequest([]byte(`{
			"model":"claude-sonnet-4-6",
			"input":"hi",
			"tools":[{"type":"web_search","filters":{"allowed_domains":["example.com"],"blocked_domains":["bad.com"]},"search_context_size":"medium","cache_control":{"type":"ephemeral"}}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		body, losses, err := anthropicmessages.EncodeRequestWith(req, anthropicmessages.EncodeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		wantTool := `{"allowed_domains":["example.com"],"name":"web_search","type":"web_search_20250305"}`
		if len(got.Tools) != 1 || string(got.Tools[0]) != wantTool {
			t.Fatalf("tools = %s, want [%s]", got.Tools, wantTool)
		}
		wantDropped := []string{"tools[0].cache_control", "tools[0].filters.blocked_domains", "tools[0].search_context_size"}
		if dropped := (&llm.Translation{Losses: losses}).DroppedPaths(); !slices.Equal(dropped, wantDropped) {
			t.Errorf("dropped = %v, want %v (losses: %+v)", dropped, wantDropped, losses)
		}
	})

	t.Run("gemini_googleSearch_inner_field_reported_dropped", func(t *testing.T) {
		req, err := geminigenerate.ParseRequest("gpt-5.4", false, []byte(`{
			"contents":[{"role":"user","parts":[{"text":"hi"}]}],
			"tools":[{"googleSearch":{"timeRangeFilter":{"startTime":"2025-01-01T00:00:00Z"}}}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		_, respLosses, err := openairesponses.EncodeRequestWith(req, openairesponses.EncodeOptions{WebSearch: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := (&llm.Translation{Losses: respLosses}).DroppedPaths(); !slices.Equal(got, []string{"tools[0].googleSearch.timeRangeFilter"}) {
			t.Errorf("responses dropped = %v, want [tools[0].googleSearch.timeRangeFilter]", got)
		}
	})

	t.Run("function_tool_with_wrongly_typed_max_uses_parses_as_function", func(t *testing.T) {
		mReq, err := anthropicmessages.ParseRequest([]byte(`{
			"model":"claude-sonnet-4-6",
			"max_tokens":1024,
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"name":"get_weather","input_schema":{"type":"object"},"max_uses":"5"}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(mReq.Tools) != 1 || !mReq.Tools[0].IsFunction() || mReq.Tools[0].Name != "get_weather" {
			t.Errorf("messages tool = %+v, want function tool get_weather", mReq.Tools)
		}

		rReq, err := openairesponses.ParseRequest([]byte(`{
			"model":"gpt-5.4",
			"input":"hi",
			"tools":[{"type":"function","name":"get_weather","parameters":{"type":"object"},"max_uses":"5"}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		if len(rReq.Tools) != 1 || !rReq.Tools[0].IsFunction() || rReq.Tools[0].Name != "get_weather" {
			t.Errorf("responses tool = %+v, want function tool get_weather", rReq.Tools)
		}
	})
}
