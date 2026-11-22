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
		if (&llm.Translation{Losses: losses}).Dropped() {
			t.Errorf("expected no dropped losses on existing Responses->Claude mapping, got %+v", losses)
		}
		var approx []string
		for _, l := range losses {
			if l.Kind == llm.LossApproximated {
				approx = append(approx, l.Path)
			}
		}
		wantApprox := []string{"tools[0].cache_control", "tools[0].filters.blocked_domains", "tools[0].search_context_size"}
		if !slices.Equal(approx, wantApprox) {
			t.Errorf("approximated = %v, want %v (losses: %+v)", approx, wantApprox, losses)
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

func TestWebSearchExternalWebAccessFalseDropsOnChatAndGemini(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5-search-api",
		"input":"hi",
		"tools":[{"type":"web_search","external_web_access":false}]
	}`)
	req, err := openairesponses.ParseRequest(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Chat target: drops tool with a dropped loss at tools[0].
	chatBody, chatLosses, err := openaichat.EncodeRequestWith(req, openaichat.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var chatGot struct {
		WebSearchOptions json.RawMessage `json:"web_search_options"`
	}
	_ = json.Unmarshal(chatBody, &chatGot)
	if len(chatGot.WebSearchOptions) != 0 {
		t.Errorf("chat: unexpected web_search_options: %s", chatGot.WebSearchOptions)
	}
	if dropped := (&llm.Translation{Losses: chatLosses}).DroppedPaths(); !slices.Equal(dropped, []string{"tools[0]"}) {
		t.Errorf("chat dropped = %v, want [tools[0]] (losses: %+v)", dropped, chatLosses)
	}

	// Gemini target: drops tool with a dropped loss at tools[0].
	reqG := req
	reqG.Model = "gemini-2.5-flash"
	gemBody, gemLosses, err := geminigenerate.EncodeRequestWith(reqG, geminigenerate.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var gemGot struct {
		Tools []any `json:"tools"`
	}
	_ = json.Unmarshal(gemBody, &gemGot)
	if len(gemGot.Tools) != 0 {
		t.Errorf("gemini: unexpected tools: %v", gemGot.Tools)
	}
	if dropped := (&llm.Translation{Losses: gemLosses}).DroppedPaths(); !slices.Equal(dropped, []string{"tools[0]"}) {
		t.Errorf("gemini dropped = %v, want [tools[0]] (losses: %+v)", dropped, gemLosses)
	}

	// Responses-to-Claude target: stays as at base (no tool, no losses).
	reqC := req
	reqC.Model = "claude-sonnet-4-6"
	claudeBody, claudeLosses, err := anthropicmessages.EncodeRequestWith(reqC, anthropicmessages.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var claudeGot struct {
		Tools []any `json:"tools"`
	}
	_ = json.Unmarshal(claudeBody, &claudeGot)
	if len(claudeGot.Tools) != 0 || len(claudeLosses) != 0 {
		t.Errorf("claude: tools=%v losses=%+v, want empty", claudeGot.Tools, claudeLosses)
	}
}

func TestWebSearchBlockedDomainsResponsesAndClaude(t *testing.T) {
	// 1. Responses parser reads filters.blocked_domains into WebSearch.BlockedDomains.
	rReq, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"hi",
		"tools":[{"type":"web_search_preview","filters":{"blocked_domains":["bad.com"]}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(rReq.Tools) != 1 || rReq.Tools[0].WebSearch == nil || !slices.Equal(rReq.Tools[0].WebSearch.BlockedDomains, []string{"bad.com"}) {
		t.Fatalf("parsed BlockedDomains = %+v, want [bad.com]", rReq.Tools)
	}
	// And Claude target carries blocked_domains with no loss.
	cBody, cLosses, err := anthropicmessages.EncodeRequestWith(rReq, anthropicmessages.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var cGot struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(cBody, &cGot)
	if len(cGot.Tools) != 1 || cGot.Tools[0]["blocked_domains"] == nil || len(cLosses) != 0 {
		t.Errorf("responses_preview -> claude blocked_domains: tools=%v losses=%+v", cGot.Tools, cLosses)
	}

	// 2. Messages blocked_domains -> Responses writes filters.blocked_domains with no loss.
	mBlocked, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","blocked_domains":["reddit.com","quora.com"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	respBody, respLosses, err := openairesponses.EncodeRequestWith(mBlocked, openairesponses.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var respGot struct {
		Tools []struct {
			Type    string `json:"type"`
			Filters struct {
				AllowedDomains []string `json:"allowed_domains"`
				BlockedDomains []string `json:"blocked_domains"`
			} `json:"filters"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(respBody, &respGot)
	if len(respGot.Tools) != 1 || !slices.Equal(respGot.Tools[0].Filters.BlockedDomains, []string{"reddit.com", "quora.com"}) {
		t.Errorf("messages -> responses blocked_domains: tools=%+v (body: %s)", respGot.Tools, respBody)
	}
	if len(respLosses) != 0 {
		t.Errorf("messages -> responses blocked_domains: unexpected losses %+v", respLosses)
	}

	// 3. Messages with BOTH allowed_domains and blocked_domains -> Responses writes allowed_domains only and drops blocked_domains.
	mBoth, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5.4",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","allowed_domains":["a.com"],"blocked_domains":["b.com"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	respBothBody, respBothLosses, err := openairesponses.EncodeRequestWith(mBoth, openairesponses.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var respBothGot struct {
		Tools []struct {
			Filters struct {
				AllowedDomains []string `json:"allowed_domains"`
				BlockedDomains []string `json:"blocked_domains"`
			} `json:"filters"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(respBothBody, &respBothGot)
	if len(respBothGot.Tools) != 1 || !slices.Equal(respBothGot.Tools[0].Filters.AllowedDomains, []string{"a.com"}) || len(respBothGot.Tools[0].Filters.BlockedDomains) != 0 {
		t.Errorf("both -> responses: tools=%+v (body: %s)", respBothGot.Tools, respBothBody)
	}
	if dropped := (&llm.Translation{Losses: respBothLosses}).DroppedPaths(); !slices.Equal(dropped, []string{"tools[0].blocked_domains"}) {
		t.Errorf("both -> responses dropped = %v, want [tools[0].blocked_domains]", dropped)
	}

	// 4. Both allowed_domains and blocked_domains -> Claude writes allowed_domains only and drops blocked_domains.
	rBoth, err := openairesponses.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"input":"hi",
		"tools":[{"type":"web_search_preview","filters":{"allowed_domains":["a.com"],"blocked_domains":["b.com"]}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	cBothBody, cBothLosses, err := anthropicmessages.EncodeRequestWith(rBoth, anthropicmessages.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var cBothGot struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(cBothBody, &cBothGot)
	if len(cBothGot.Tools) != 1 || cBothGot.Tools[0]["allowed_domains"] == nil || cBothGot.Tools[0]["blocked_domains"] != nil {
		t.Errorf("both -> claude: tools=%v (body: %s)", cBothGot.Tools, cBothBody)
	}
	if dropped := (&llm.Translation{Losses: cBothLosses}).DroppedPaths(); !slices.Equal(dropped, []string{"tools[0].filters.blocked_domains"}) {
		t.Errorf("both -> claude dropped = %v, want [tools[0].filters.blocked_domains]", dropped)
	}
}

func TestWebSearchUserLocationValidation(t *testing.T) {
	// Chat -> Claude: invalid type and non-2-letter country are normalised/dropped with losses.
	chatReq, err := openaichat.ParseRequest([]byte(`{
		"model":"claude-sonnet-4-6",
		"messages":[{"role":"user","content":"hi"}],
		"web_search_options":{"user_location":{"type":"exact","approximate":{"country":"France","city":"Paris"}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	cBody, cLosses, err := anthropicmessages.EncodeRequestWith(chatReq, anthropicmessages.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var cGot struct {
		Tools []struct {
			UserLocation map[string]any `json:"user_location"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(cBody, &cGot)
	if len(cGot.Tools) != 1 {
		t.Fatalf("claude tools = %+v", cGot.Tools)
	}
	if loc := cGot.Tools[0].UserLocation; loc["type"] != "approximate" || loc["country"] != nil || loc["city"] != "Paris" {
		t.Errorf("claude user_location = %v, want type=approximate, no country, city=Paris", loc)
	}
	wantChatDropped := []string{"web_search_options.user_location.approximate.country", "web_search_options.user_location.type"}
	if dropped := (&llm.Translation{Losses: cLosses}).DroppedPaths(); !slices.Equal(dropped, wantChatDropped) {
		t.Errorf("claude dropped = %v, want %v", dropped, wantChatDropped)
	}

	// Messages -> Responses & Chat: invalid type and non-2-letter country.
	msgReq, err := anthropicmessages.ParseRequest([]byte(`{
		"model":"gpt-5-search-api",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search_20250305","name":"web_search","user_location":{"type":"exact","country":"France","city":"Paris"}}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	rBody, rLosses, err := openairesponses.EncodeRequestWith(msgReq, openairesponses.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var rGot struct {
		Tools []struct {
			UserLocation map[string]any `json:"user_location"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(rBody, &rGot)
	if len(rGot.Tools) != 1 || rGot.Tools[0].UserLocation["type"] != "approximate" || rGot.Tools[0].UserLocation["country"] != nil || rGot.Tools[0].UserLocation["city"] != "Paris" {
		t.Errorf("responses user_location = %+v, want type=approximate, no country, city=Paris", rGot.Tools)
	}
	wantMsgDropped := []string{"tools[0].user_location.country", "tools[0].user_location.type"}
	if dropped := (&llm.Translation{Losses: rLosses}).DroppedPaths(); !slices.Equal(dropped, wantMsgDropped) {
		t.Errorf("responses dropped = %v, want %v", dropped, wantMsgDropped)
	}

	chBody, chLosses, err := openaichat.EncodeRequestWith(msgReq, openaichat.EncodeOptions{WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	var chGot struct {
		WebSearchOptions struct {
			UserLocation struct {
				Type        string         `json:"type"`
				Approximate map[string]any `json:"approximate"`
			} `json:"user_location"`
		} `json:"web_search_options"`
	}
	_ = json.Unmarshal(chBody, &chGot)
	if chGot.WebSearchOptions.UserLocation.Type != "approximate" || chGot.WebSearchOptions.UserLocation.Approximate["country"] != nil || chGot.WebSearchOptions.UserLocation.Approximate["city"] != "Paris" {
		t.Errorf("chat user_location = %+v, want type=approximate, no country, city=Paris", chGot.WebSearchOptions.UserLocation)
	}
	if dropped := (&llm.Translation{Losses: chLosses}).DroppedPaths(); !slices.Equal(dropped, wantMsgDropped) {
		t.Errorf("chat dropped = %v, want %v", dropped, wantMsgDropped)
	}
}
