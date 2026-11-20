package main

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
)

// Web search result owners. Anthropic encrypts a web_search_result's encrypted_content for the
// credential that ran the search and refuses it from another credential (a 400 naming the
// search_result block), while routing can send a client's history to any of the extension's
// accounts. This file records which account received each encrypted value, so a replay can leave
// out the results another account minted. Only a hash of the value is kept, never the value.
//
// A value this process has not seen is treated as not the account's: keeping it would bring back
// the permanent 400 whenever the account changed across a restart, while leaving it out only
// loses that one search result from the replayed context.
//
// shortcut: owners live in the extension's memory, so they are gone when core stops the extension
// after 10 minutes without requests, or restarts it. Move them to a store that outlives the
// program (the per-extension data folder the protocol plans) if that shows up in use. Requests
// that arrive in Claude Messages format are passed through unchanged, so their history may hold
// values this process never saw and this file never filters.

// searchOwnerMax bounds the owners kept, as today's core bounds its equivalent cache. Each entry
// is a 32-byte hash and an account ID.
const searchOwnerMax = 16384

// searchOwners keeps, by a hash of the encrypted value, the account that received it.
type searchOwnersCache struct {
	mu    sync.Mutex
	owner map[[sha256.Size]byte]string
	ring  [][sha256.Size]byte // the keys in insertion order, oldest at next
	next  int
}

var searchOwners searchOwnersCache

// remember records that account received enc, a web search result's encrypted_content.
func (c *searchOwnersCache) remember(enc, account string) {
	if enc == "" || account == "" {
		return
	}
	key := sha256.Sum256([]byte(enc))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.owner == nil {
		c.owner = map[[sha256.Size]byte]string{}
		c.ring = make([][sha256.Size]byte, searchOwnerMax)
	}
	if _, kept := c.owner[key]; kept {
		c.owner[key] = account
		return
	}
	old := c.ring[c.next]
	delete(c.owner, old)
	c.ring[c.next] = key
	c.next = (c.next + 1) % len(c.ring)
	c.owner[key] = account
}

// mintedBy reports whether enc was received by account. A value no reply recorded, or one this
// process dropped from its memory, is not the account's.
func (c *searchOwnersCache) mintedBy(enc, account string) bool {
	if enc == "" || account == "" {
		return false
	}
	key := sha256.Sum256([]byte(enc))
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.owner[key] == account
}

// searchResultBlock is the part of a Messages content_block_start that names a web search result.
type searchResultBlock struct {
	Type         string `json:"type"`
	ContentBlock struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	} `json:"content_block"`
}

// rememberSearchResults records account against every web search result's encrypted_content in
// raw, one event of a Messages reply (non-streamed or streamed). Anything else is ignored.
func rememberSearchResults(raw json.RawMessage, account string) {
	if account == "" || len(raw) == 0 {
		return
	}
	var ev searchResultBlock
	if json.Unmarshal(raw, &ev) != nil || ev.Type != "content_block_start" || ev.ContentBlock.Type != "web_search_tool_result" {
		return
	}
	var entries []struct {
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal(ev.ContentBlock.Content, &entries) != nil {
		return
	}
	for _, e := range entries {
		searchOwners.remember(e.EncryptedContent, account)
	}
}
