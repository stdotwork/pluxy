package llm

// WebSearchTool holds the settings of a canonical web search tool (protocol minor version 29)
// that more than one format can say. The client's original tool stays on Tool.Origin.
type WebSearchTool struct {
	MaxUses           *int          `json:"max_uses,omitempty"`
	AllowedDomains    []string      `json:"allowed_domains,omitempty"`
	BlockedDomains    []string      `json:"blocked_domains,omitempty"`
	UserLocation      *UserLocation `json:"user_location,omitempty"`
	SearchContextSize string        `json:"search_context_size,omitempty"`
	// Disabled is true when the client explicitly asked for no live web search (Responses
	// external_web_access: false).
	Disabled bool `json:"disabled,omitempty"`
}

// UserLocation is the approximate location for a web search tool.
type UserLocation struct {
	Type     string `json:"type,omitempty"`
	Country  string `json:"country,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	Timezone string `json:"timezone,omitempty"`
}

// WebSearchUnsent is the detail of the loss for a web search tool an encoder leaves out because
// nothing says its upstream takes one.
const WebSearchUnsent = "nothing says this upstream takes a web search tool, so it is not sent"
