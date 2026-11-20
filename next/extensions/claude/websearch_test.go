package main

import (
	"encoding/json"
	"testing"
)

// TestRememberSearchResults checks which web search results a reply's event records for its
// account: the entries of a web_search_tool_result block, and nothing from another event.
func TestRememberSearchResults(t *testing.T) {
	searchOwners = searchOwnersCache{}
	block := json.RawMessage(`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result",` +
		`"tool_use_id":"srvtoolu_a","content":[` +
		`{"type":"web_search_result","url":"https://example.com/a","encrypted_content":"enc-a"},` +
		`{"type":"web_search_result","url":"https://example.com/b","encrypted_content":"enc-b"}]}}`)
	rememberSearchResults(block, "account-a")
	rememberSearchResults(json.RawMessage(`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`), "account-a")
	rememberSearchResults(json.RawMessage(`not json`), "account-a")

	for _, enc := range []string{"enc-a", "enc-b"} {
		if !searchOwners.mintedBy(enc, "account-a") {
			t.Errorf("%s: not recorded for the account that received it", enc)
		}
		if searchOwners.mintedBy(enc, "account-b") {
			t.Errorf("%s: reported as minted for another account", enc)
		}
	}
	if !searchOwners.mintedBy("enc-c", "account-a") {
		t.Error("an unrecorded value is left out")
	}
}
