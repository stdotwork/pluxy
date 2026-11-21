package openaichat

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// parseWebSearchOptions reads Chat Completions' top-level web_search_options object into a
// canonical ToolWebSearch. The client's original object stays on Origin.Raw.
func parseWebSearchOptions(raw json.RawMessage) *llm.Tool {
	var o struct {
		UserLocation *struct {
			Type        string `json:"type"`
			Approximate *struct {
				Country  string `json:"country"`
				City     string `json:"city"`
				Region   string `json:"region"`
				Timezone string `json:"timezone"`
			} `json:"approximate"`
			Country  string `json:"country"`
			City     string `json:"city"`
			Region   string `json:"region"`
			Timezone string `json:"timezone"`
		} `json:"user_location"`
	}
	ws := &llm.WebSearchTool{}
	if json.Unmarshal(raw, &o) == nil && o.UserLocation != nil {
		u := &llm.UserLocation{Type: o.UserLocation.Type}
		if a := o.UserLocation.Approximate; a != nil {
			u.Country, u.City, u.Region, u.Timezone = a.Country, a.City, a.Region, a.Timezone
		} else {
			u.Country, u.City, u.Region, u.Timezone = o.UserLocation.Country, o.UserLocation.City, o.UserLocation.Region, o.UserLocation.Timezone
		}
		ws.UserLocation = u
	}
	return &llm.Tool{
		Type:      llm.ToolWebSearch,
		WebSearch: ws,
		Origin:    &llm.Origin{Format: format, Path: "web_search_options", Raw: append(json.RawMessage(nil), raw...)},
	}
}

// isSearchModel reports whether model is one of OpenAI's specialized Chat Completions search
// models (such as gpt-5-search-api, or the legacy gpt-4o-search-preview / gpt-4o-mini-search-preview).
// Per https://developers.openai.com/api/docs/guides/tools-web-search, only these models accept
// web_search_options on the Chat Completions API.
func isSearchModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(m, "-search-api") || strings.Contains(m, "-search-preview")
}
