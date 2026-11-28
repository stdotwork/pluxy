package anthropicmessages

import (
	"cmp"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

const webSearchToolType = "web_search_20250305"

// serverToolIDPrefix starts every Claude server tool ID; Anthropic takes only
// ^srvtoolu_[a-zA-Z0-9_]+$.
const serverToolIDPrefix = "srvtoolu_"

func webSearchTool(t llm.Tool) (tool json.RawMessage, skip bool, carried rawjson.WebSearchCarried, ok bool) {
	if t.Origin == nil || t.Origin.Format != llm.FormatOpenAIResponses {
		return nil, false, carried, false
	}
	var w struct {
		Type              string          `json:"type"`
		Name              string          `json:"name"`
		ExternalWebAccess *bool           `json:"external_web_access"`
		MaxUses           *int64          `json:"max_uses"`
		UserLocation      json.RawMessage `json:"user_location"`
		Filters           struct {
			AllowedDomains json.RawMessage `json:"allowed_domains"`
		} `json:"filters"`
	}
	if json.Unmarshal(t.Origin.Raw, &w) != nil || w.Type != "web_search" {
		return nil, false, carried, false
	}
	if w.ExternalWebAccess != nil && !*w.ExternalWebAccess {
		return nil, true, carried, true
	}
	out := map[string]any{"type": webSearchToolType, "name": cmp.Or(strings.TrimSpace(w.Name), "web_search")}
	if w.MaxUses != nil {
		out["max_uses"] = *w.MaxUses
		carried.MaxUses = true
	}
	if a := w.Filters.AllowedDomains; len(a) > 0 && a[0] == '[' {
		out["allowed_domains"] = a
		carried.AllowedDomains = true
	}
	if u := w.UserLocation; len(u) > 0 && u[0] == '{' {
		out["user_location"] = u
		carried.UserLocation = true
		carried.RawUserLocation = true
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false, carried, false
	}
	return b, false, carried, true
}

func supportsWebSearch(model string) bool {
	m := claudeName(model)
	if m == "claude-mythos-preview" || strings.HasPrefix(m, "claude-mythos-preview-") {
		return true
	}
	major, minor, ok := parseClaudeVersion(model)
	return ok && (major > 4 || (major == 4 && minor >= 5))
}

func canonicalWebSearchTool(t llm.Tool) (tool json.RawMessage, skip bool, carried rawjson.WebSearchCarried, ok bool) {
	if t.WebSearch != nil && t.WebSearch.Disabled {
		return nil, true, carried, true
	}
	out := map[string]any{"type": webSearchToolType, "name": cmp.Or(strings.TrimSpace(t.Name), "web_search")}
	if ws := t.WebSearch; ws != nil {
		if ws.MaxUses != nil {
			out["max_uses"] = *ws.MaxUses
			carried.MaxUses = true
		}
		if len(ws.AllowedDomains) > 0 {
			out["allowed_domains"] = ws.AllowedDomains
			carried.AllowedDomains = true
		} else if len(ws.BlockedDomains) > 0 {
			out["blocked_domains"] = ws.BlockedDomains
			carried.BlockedDomains = true
		}
		if u := ws.UserLocation; u != nil {
			loc := map[string]any{"type": "approximate"}
			hasAny := false
			if rawjson.IsCountryCode(u.Country) {
				loc["country"] = u.Country
				hasAny = true
			}
			if u.City != "" {
				loc["city"] = u.City
				hasAny = true
			}
			if u.Region != "" {
				loc["region"] = u.Region
				hasAny = true
			}
			if u.Timezone != "" {
				loc["timezone"] = u.Timezone
				hasAny = true
			}
			if hasAny {
				out["user_location"] = loc
				carried.UserLocation = true
			}
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false, carried, false
	}
	return b, false, carried, true
}

type webSearchCall struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Action struct {
		Query   string   `json:"query"`
		Queries []string `json:"queries"`
		URL     string   `json:"url"`
	} `json:"action"`
	Results json.RawMessage `json:"results"`
}

func asWebSearchCall(it llm.Item) (c webSearchCall, ok bool) {
	if it.Type != llm.ItemTypeNative || it.Origin == nil || it.Origin.Format != llm.FormatOpenAIResponses {
		return c, false
	}
	m := rawjson.Object(it.Origin.Raw)
	var typ string
	if json.Unmarshal(m["type"], &typ) != nil || typ != "web_search_call" || json.Unmarshal(it.Origin.Raw, &c) != nil {
		return c, false
	}
	c.ID = strings.TrimSpace(c.ID)
	return c, true
}

func (c webSearchCall) query() string {
	if q := strings.TrimSpace(c.Action.Query); q != "" {
		return q
	}
	if len(c.Action.Queries) > 0 {
		if q := strings.TrimSpace(c.Action.Queries[0]); q != "" {
			return q
		}
	}
	return strings.TrimSpace(c.Action.URL)
}

func (c webSearchCall) paused() bool {
	return strings.EqualFold(strings.TrimSpace(c.Status), "in_progress") && len(c.Results) == 0
}

func serverToolID(itemID string) string {
	body := strings.TrimPrefix(strings.TrimPrefix(itemID, "ws_"), serverToolIDPrefix)
	b := []byte(body)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			b[i] = '_'
		}
	}
	if len(b) == 0 {
		return ""
	}
	return serverToolIDPrefix + string(b)
}

func webSearchResults(raw json.RawMessage, owned func(string) bool) (json.RawMessage, int) {
	if len(raw) > 0 && raw[0] == '{' {
		return raw, 0
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil, 0
	}
	var out []map[string]json.RawMessage
	dropped := 0
	for _, e := range entries {
		var typ, enc string
		_ = json.Unmarshal(e["type"], &typ)
		_ = json.Unmarshal(e["encrypted_content"], &enc)
		switch {
		case typ == "web_search_tool_result_error":
			out = append(out, e)
		case strings.TrimSpace(enc) != "":
			if owned != nil && !owned(enc) {
				dropped++
				continue
			}
			e["type"] = json.RawMessage(`"web_search_result"`)
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, dropped
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, dropped
	}
	return b, dropped
}

type webSearchHistory struct {
	skip  map[int]bool
	query map[int]string
	tail  map[int]bool
}

// boundary reports whether it is a user turn. Tool results do not end the assistant's turn:
// Anthropic runs a deferred search right after them.
func boundary(it llm.Item) bool {
	return it.Type == llm.ItemTypeMessage && it.Role != llm.RoleAssistant
}

func scanWebSearches(items []llm.Item) webSearchHistory {
	h := webSearchHistory{skip: map[int]bool{}, query: map[int]string{}, tail: map[int]bool{}}
	calls := make([]webSearchCall, len(items))
	isCall := make([]bool, len(items))
	next := make([]int, len(items))
	later := map[string]int{}
	after := false
	for i := len(items) - 1; i >= 0; i-- {
		h.tail[i] = !after
		next[i] = -1
		if boundary(items[i]) {
			after = true
			clear(later)
			continue
		}
		c, ok := asWebSearchCall(items[i])
		if !ok {
			continue
		}
		calls[i], isCall[i] = c, true
		if j, found := later[c.ID]; found {
			next[i] = j
		}
		later[c.ID] = i
	}
	for i := range items {
		c, j := calls[i], next[i]
		if !isCall[i] || !c.paused() || c.ID == "" || j < 0 {
			continue
		}
		h.skip[i] = true
		q := c.query()
		if prev, ok := h.query[i]; ok && q == "" {
			q = prev
		}
		h.query[j] = q
	}
	return h
}

func (h webSearchHistory) blocks(i int, c webSearchCall, owned func(string) bool) ([]json.RawMessage, int) {
	id := serverToolID(c.ID)
	if h.skip[i] || id == "" {
		return nil, 0
	}
	q := c.query()
	if q == "" {
		q = h.query[i]
	}
	input := map[string]any{}
	if q != "" {
		input["query"] = q
	}
	use, _ := json.Marshal(map[string]any{"type": "server_tool_use", "id": id, "name": "web_search", "input": input})
	if c.paused() && h.tail[i] {
		return []json.RawMessage{use}, 0
	}
	content, dropped := webSearchResults(c.Results, owned)
	if content == nil {
		content = json.RawMessage("[]")
	}
	result, _ := json.Marshal(map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": content})
	return []json.RawMessage{use, result}, dropped
}
