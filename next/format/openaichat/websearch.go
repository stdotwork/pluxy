package openaichat

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

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
	if s, ok := rawjson.Text(rawjson.Object(raw)["search_context_size"]); ok {
		ws.SearchContextSize = s
	}
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

func isSearchModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(m, "-search-api") || strings.Contains(m, "-search-preview")
}
