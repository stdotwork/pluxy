package main

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
)

const searchOwnerMax = 16384

type searchOwnersCache struct {
	mu    sync.Mutex
	owner map[[sha256.Size]byte]string
	ring  [][sha256.Size]byte
	next  int
}

var searchOwners searchOwnersCache

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

func (c *searchOwnersCache) mintedBy(enc, account string) bool {
	if enc == "" || account == "" {
		return true
	}
	key := sha256.Sum256([]byte(enc))
	c.mu.Lock()
	defer c.mu.Unlock()
	owner, recorded := c.owner[key]
	return !recorded || owner == account
}

type searchResultBlock struct {
	Type         string `json:"type"`
	ContentBlock struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	} `json:"content_block"`
}

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
