package openairesponses

import (
	"encoding/json"
	"strings"

	"github.com/stdotwork/pluxy/next/format/internal/rawjson"
	"github.com/stdotwork/pluxy/next/llm"
)

// Claude's server web search, for a Responses client. Claude reports a search as a
// server_tool_use block (named web_search, its input holding the query) and a
// web_search_tool_result block (the hits); the model has no item for either, so they reach the
// writer as native events in Messages form. A Responses client gets them as one web_search_call
// item, as today's core gives it: added when the search starts, done once its results are in
// (when the next block starts) or when the reply ends. A search left without results when Claude
// paused the turn (pause_turn) or stopped for a tool call stays in_progress, so the client sends it
// back without results and Claude carries on. The Messages request builder turns a replayed
// web_search_call back into the two blocks.

// itemWebSearch is the kind of an item rebuilt from Claude's web search blocks.
const itemWebSearch = "web_search_call"

// claudeBlockEvent is the part of a Messages stream event a web search needs.
type claudeBlockEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

// searches tracks the web searches of one reply.
type searches struct {
	byBlock map[int]*item    // open server_tool_use blocks, by upstream block index
	byID    map[string]*item // every search, by Claude's server tool ID
	open    []*item          // searches not yet done, in order
}

// searchEvent reads a native event of a reply from a Messages upstream. It returns the search a
// server_tool_use or web_search_tool_result block starts (added true: the item is new), or
// consumed true for an event that only feeds a search already open; an event that has nothing to
// do with web search gives neither.
func (b *builder) searchEvent(raw json.RawMessage) (it *item, added, consumed bool) {
	if b.upFormat != llm.FormatAnthropicMessages {
		return nil, false, false
	}
	var ev claudeBlockEvent
	if json.Unmarshal(raw, &ev) != nil {
		return nil, false, false
	}
	s := &b.search
	switch ev.Type {
	case "content_block_start":
		cb := ev.ContentBlock
		if cb == nil {
			return nil, false, false
		}
		switch {
		case cb.Type == "server_tool_use" && cb.Name == "web_search" && cb.ID != "":
			it = b.openSearch(cb.ID)
			it.input.Write(cb.Input)
			s.byBlock[ev.Index] = it
			return it, true, true
		case cb.Type == "web_search_tool_result" && cb.ToolUseID != "":
			it = s.byID[cb.ToolUseID]
			if it == nil {
				// The result of a search whose server_tool_use came in the reply Claude
				// paused: the client gets it under the same ID.
				it = b.openSearch(cb.ToolUseID)
				added = true
			}
			it.results = searchResults(cb.Content)
			it.hasResults = true
			return it, added, true
		}
	case "content_block_delta":
		if it := s.byBlock[ev.Index]; it != nil && ev.Delta != nil && ev.Delta.Type == "input_json_delta" {
			if !it.streamed {
				// A streamed block starts with an empty input; its deltas hold the query.
				it.input.Reset()
				it.streamed = true
			}
			it.input.WriteString(ev.Delta.PartialJSON)
			return nil, false, true
		}
	case "content_block_stop":
		if it := s.byBlock[ev.Index]; it != nil {
			delete(s.byBlock, ev.Index)
			var in struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal([]byte(it.input.String()), &in)
			it.query = strings.TrimSpace(in.Query)
			it.input = strings.Builder{} // read once; the query is what is kept
			return nil, false, true
		}
	}
	return nil, false, false
}

func (b *builder) openSearch(id string) *item {
	s := &b.search
	if s.byID == nil {
		s.byID, s.byBlock = map[string]*item{}, map[int]*item{}
	}
	it := &item{kind: itemWebSearch, id: "ws_" + id, out: b.nextOut}
	b.nextOut++
	b.order = append(b.order, it)
	s.byID[id] = it
	s.open = append(s.open, it)
	return it
}

// readySearches returns the open searches that have their results and marks them done: they end
// when the next block starts.
func (b *builder) readySearches() []*item {
	var ready, still []*item
	for _, it := range b.search.open {
		if it.hasResults {
			it.status, it.done = "completed", true
			ready = append(ready, it)
		} else {
			still = append(still, it)
		}
	}
	b.search.open = still
	return ready
}

// endSearches marks every open search done at the end of the reply and returns them. A search
// without results stays in_progress when Claude paused the turn or stopped for a tool call.
func (b *builder) endSearches() []*item {
	ended := b.search.open
	b.search.open = nil
	for _, it := range ended {
		it.status, it.done = b.itemStatus(), true
		if b.finish != nil && !it.hasResults && (b.finish.RawReason == "pause_turn" || b.finish.RawReason == "tool_use") {
			it.status = "in_progress"
		}
	}
	return ended
}

// searchItem is a web_search_call item; added gives it as response.output_item.added does.
func searchItem(it *item, added bool) obj {
	o := obj{"id": it.id, "type": "web_search_call", "status": it.status, "action": obj{"type": "search", "query": it.query}}
	if added {
		o["status"] = "in_progress"
		return o
	}
	if len(it.results) > 0 {
		o["results"] = it.results
	}
	return o
}

// searchResults turns the content of a web_search_tool_result block into a web_search_call's
// results: each hit (a result with a URL) and each error entry as Claude sent it, so the hit's
// encrypted_content, which Claude needs to replay it, survives; an error object as it is.
func searchResults(content json.RawMessage) json.RawMessage {
	if len(content) > 0 && content[0] == '{' {
		return content
	}
	var entries []json.RawMessage
	if json.Unmarshal(content, &entries) != nil {
		return nil
	}
	out := []json.RawMessage{}
	for _, e := range entries {
		m := rawjson.Object(e)
		var typ, url string
		_ = json.Unmarshal(m["type"], &typ)
		_ = json.Unmarshal(m["url"], &url)
		if typ == "web_search_tool_result_error" || strings.TrimSpace(url) != "" {
			out = append(out, e)
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// isBlockStart reports whether raw is a Messages content_block_start event.
func isBlockStart(raw json.RawMessage) bool {
	var ev struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &ev) == nil && ev.Type == "content_block_start"
}
