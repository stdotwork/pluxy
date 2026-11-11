package anthropicmessages

import (
	"cmp"
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// Web search from a Responses client. Responses has a hosted web_search tool and replays each
// search as a web_search_call item; Messages has Claude's server web search tool and replays a
// search as a server_tool_use block followed by a web_search_tool_result block. The two carry
// the same exchange, so a Responses client's tool and items become Claude's, as today's core
// does. (The Responses writer turns Claude's blocks back into web_search_call items.) A replayed
// search must carry its results: without them the model treats its earlier answer as unsourced
// and searches again.

// webSearchToolType is the Claude server web search tool a Responses web_search tool becomes.
const webSearchToolType = "web_search_20250305"

// serverToolIDPrefix starts every Claude server tool ID; Anthropic takes only
// ^srvtoolu_[a-zA-Z0-9_]+$.
const serverToolIDPrefix = "srvtoolu_"

// webSearchTool returns the Claude tool for a Responses web_search tool, or ok false when t is not
// one. A tool the client marked external_web_access false asks for no search on the web, so it
// becomes no tool (skip true). The tool's name (default web_search), max_uses,
// filters.allowed_domains and user_location carry over; Claude's tool has nothing for the rest.
func webSearchTool(t llm.Tool) (tool json.RawMessage, skip, ok bool) {
	if t.Origin == nil || t.Origin.Format != llm.FormatOpenAIResponses {
		return nil, false, false
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
		return nil, false, false
	}
	if w.ExternalWebAccess != nil && !*w.ExternalWebAccess {
		return nil, true, true
	}
	out := map[string]any{"type": webSearchToolType, "name": cmp.Or(strings.TrimSpace(w.Name), "web_search")}
	if w.MaxUses != nil {
		out["max_uses"] = *w.MaxUses
	}
	if a := w.Filters.AllowedDomains; len(a) > 0 && a[0] == '[' {
		out["allowed_domains"] = a
	}
	if u := w.UserLocation; len(u) > 0 && u[0] == '{' {
		out["user_location"] = u
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false, false
	}
	return b, false, true
}

// webSearchCall is a replayed Responses web_search_call item.
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

// asWebSearchCall reads it as a replayed Responses web_search_call, or ok false.
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

// query is the search's query: action.query, else the first of action.queries, else the page an
// open_page action opened.
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

// paused reports whether the search was still running when its turn ended (Claude's pause_turn):
// in progress and without results.
func (c webSearchCall) paused() bool {
	return strings.EqualFold(strings.TrimSpace(c.Status), "in_progress") && len(c.Results) == 0
}

// serverToolID is the Claude server tool ID for a web_search_call item ID: the ID without the
// ws_ the Responses writer puts in front, each character Anthropic does not take as _, behind
// srvtoolu_. A search from another provider (ws_0011aabb) gets one too; a Claude ID comes back
// as it was.
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

// webSearchResults turns a web_search_call's results back into the content of a
// web_search_tool_result block: each result that still has its encrypted_content (Anthropic
// refuses a result without its own) as a web_search_result, error entries as they are, an error
// object as it is. Nothing usable gives nil, which is sent as an empty list.
func webSearchResults(raw json.RawMessage) json.RawMessage {
	if len(raw) > 0 && raw[0] == '{' {
		return raw
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return nil
	}
	var out []map[string]json.RawMessage
	for _, e := range entries {
		var typ, enc string
		_ = json.Unmarshal(e["type"], &typ)
		_ = json.Unmarshal(e["encrypted_content"], &enc)
		switch {
		case typ == "web_search_tool_result_error":
			out = append(out, e)
		case strings.TrimSpace(enc) != "":
			e["type"] = json.RawMessage(`"web_search_result"`)
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return b
}

// webSearchHistory decides how each replayed web_search_call goes back, as today's core does.
// A search paused at the end of the history (pause_turn, then the client continues) goes back as
// its server_tool_use alone, which is how Anthropic continues it. A paused search whose result a
// later item with the same ID brings, before the next user turn, is left out, and that later item
// goes back whole with the paused item's query. Every other search goes back as both blocks.
type webSearchHistory struct {
	skip  map[int]bool
	query map[int]string
	tail  map[int]bool // no user turn after this item
}

// boundary reports whether it is a user turn. Tool results do not end the assistant's turn:
// Anthropic runs a deferred search right after them.
func boundary(it llm.Item) bool {
	return it.Type == llm.ItemTypeMessage && it.Role != llm.RoleAssistant
}

func scanWebSearches(items []llm.Item) webSearchHistory {
	h := webSearchHistory{skip: map[int]bool{}, query: map[int]string{}, tail: map[int]bool{}}
	// One pass from the end parses each search once and finds, for each, the next search with
	// the same ID before the next user turn.
	calls := make([]webSearchCall, len(items))
	isCall := make([]bool, len(items))
	next := make([]int, len(items))
	later := map[string]int{} // nearest later search of each ID in this turn
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

// blocks returns the Claude blocks for item i, a web_search_call, or none when it is left out or
// has no usable ID.
func (h webSearchHistory) blocks(i int, c webSearchCall) []json.RawMessage {
	id := serverToolID(c.ID)
	if h.skip[i] || id == "" {
		return nil
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
		return []json.RawMessage{use}
	}
	content := webSearchResults(c.Results)
	if content == nil {
		content = json.RawMessage("[]")
	}
	result, _ := json.Marshal(map[string]any{"type": "web_search_tool_result", "tool_use_id": id, "content": content})
	return []json.RawMessage{use, result}
}
