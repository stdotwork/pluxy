package main

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
)

// Thinking replay. Kimi's Messages API wants an assistant tool-use turn back with its signed
// thinking, and some Messages clients resend earlier turns without their thinking blocks. As
// today's core does, the extension keeps each Messages reply that has signed thinking and a tool
// call, and when a later request in the same conversation resends that turn without thinking
// but otherwise exactly as it was, it puts the kept content back before sending the request.
//
// What is kept is scoped by the request's caller (one client never gets another's reasoning),
// the conversation (Claude Code's session ID) and the model family, as today's core scopes it.
// A request without a caller or a session ID is sent as it is. Only Messages clients are
// covered, as in today's core: a Chat client's tool calls get reasoning_content in fixToolTurns.
//
// shortcut: the kept turns live in the extension's memory, so they are gone when core stops the
// extension after 10 minutes without requests, or restarts it; a turn resent after that goes to
// Kimi without its thinking, as it would without this cache. Move them to a store that outlives
// the program (the per-extension data folder the protocol plans) if that shows up in use.

// Bounds, as today's core's: kept content expires an hour after it was last used, and the cache
// holds at most replayMaxEntries turns and replayMaxBytes of content and keys (a key holds the
// client's session ID, which the client chooses), dropping the least recently used first. A turn
// over replayMaxEntryBytes, key included, or replayMaxBlocks is not kept.
const (
	replayTTL           = time.Hour
	replayMaxEntries    = 10240
	replayMaxBytes      = 256 << 20
	replayMaxEntryBytes = 8 << 20
	replayMaxBlocks     = 512
)

var replays = newReplayCache()

// replayCache holds kept assistant content by key (replayTurn.key).
type replayCache struct {
	mu                   sync.Mutex
	entries              map[string]*list.Element
	order                *list.List // of *replayEntry, most recently used first
	bytes                int
	maxEntries, maxBytes int
	now                  func() time.Time
}

type replayEntry struct {
	key     string
	content []byte
	used    time.Time
}

func newReplayCache() *replayCache {
	return &replayCache{entries: map[string]*list.Element{}, order: list.New(), maxEntries: replayMaxEntries, maxBytes: replayMaxBytes, now: time.Now}
}

func (c *replayCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*replayEntry)
	now := c.now()
	if now.Sub(e.used) >= replayTTL {
		c.remove(el)
		return nil, false
	}
	e.used = now
	c.order.MoveToFront(el)
	return e.content, true
}

func (c *replayCache) put(key string, content []byte) {
	if len(key)+len(content) > replayMaxEntryBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.remove(el)
	}
	now := c.now()
	c.entries[key] = c.order.PushFront(&replayEntry{key: key, content: content, used: now})
	c.bytes += len(key) + len(content)
	for el := c.order.Back(); el != nil; el = c.order.Back() {
		e := el.Value.(*replayEntry)
		if c.order.Len() <= c.maxEntries && c.bytes <= c.maxBytes && now.Sub(e.used) < replayTTL {
			break
		}
		c.remove(el)
	}
}

// drop removes key's entry if it still holds content.
func (c *replayCache) drop(key string, content []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok && bytes.Equal(el.Value.(*replayEntry).content, content) {
		c.remove(el)
	}
}

func (c *replayCache) remove(el *list.Element) {
	e := c.order.Remove(el).(*replayEntry)
	delete(c.entries, e.key)
	c.bytes -= len(e.key) + len(e.content)
}

// replayTurn is the replay state of one request: its scope, and the kept content it put back.
type replayTurn struct {
	cache    *replayCache
	scope    string
	used     string // the key of the entry put back, "" when none
	restored []byte
}

// newReplayTurn returns the replay state of a Messages request sent to Kimi's Messages API, or
// nil when it has no caller or session ID.
func newReplayTurn(cache *replayCache, req llm.Request) *replayTurn {
	if req.Caller == "" || req.Native == nil || req.Native.Format != llm.FormatAnthropicMessages {
		return nil
	}
	session := claudeCodeSession(req.Native.Headers["x-claude-code-session-id"], req.Native.Body)
	if session == "" {
		return nil
	}
	scope := strings.Join([]string{req.Caller, replayFamily(req.Model), session}, "\x00")
	return &replayTurn{cache: cache, scope: scope}
}

// sessionSuffix is how Claude Code's older metadata.user_id ends: _session_<id>.
var sessionSuffix = regexp.MustCompile(`_session_([a-f0-9-]+)$`)

// claudeCodeSession returns a Claude Code conversation's session ID, from its
// X-Claude-Code-Session-Id header or else its body's metadata.user_id, which Claude Code writes
// either as user_<...>_session_<id> or as a JSON object with session_id.
func claudeCodeSession(header string, body []byte) string {
	if s := strings.TrimSpace(header); s != "" {
		return s
	}
	var b struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(body, &b) != nil {
		return ""
	}
	id := b.Metadata.UserID
	if m := sessionSuffix.FindStringSubmatch(id); m != nil {
		return m[1]
	}
	if strings.HasPrefix(id, "{") {
		var u struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(id), &u) == nil {
			return strings.TrimSpace(u.SessionID)
		}
	}
	return ""
}

// replayFamily is the model family kept content is shared in: Kimi's name for the model, with
// the variants of one model together, as today's core groups them.
func replayFamily(model string) string {
	switch m := strings.ToLower(upstreamModel(model)); m {
	case "k3", "k3-256k":
		return "k3"
	case "k3.1-pro", "k3.1-pro-highspeed":
		return "k3.1-pro"
	default:
		return m
	}
}

// key returns the cache key of an assistant turn in this scope, given the canonical form of the
// turn's content without its thinking blocks.
func (t *replayTurn) key(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return t.scope + "\x00" + string(sum[:])
}

// restore puts kept content back into the newest assistant turn of the Messages body m that has
// a tool call, no thinking, and a kept counterpart whose content without thinking is the same.
func (t *replayTurn) restore(m map[string]any) {
	if t == nil {
		return
	}
	msgs, _ := m["messages"].([]any)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, _ := msgs[i].(map[string]any)
		role, _ := msg["role"].(string)
		if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok || hasThinking(content) {
			continue
		}
		canonical, ok := withoutThinking(content)
		if !ok {
			continue
		}
		key := t.key(canonical)
		kept, ok := t.cache.get(key)
		if !ok {
			continue
		}
		parts, err := decodeArray(kept)
		if err != nil {
			continue
		}
		msg["content"] = parts
		t.used, t.restored = key, kept
		return
	}
}

// keep stores an assistant reply's content array (raw JSON) when it has signed thinking and a
// tool call.
func (t *replayTurn) keep(content []byte) {
	if t == nil || len(content) > replayMaxEntryBytes {
		return
	}
	parts, err := decodeArray(content)
	if err != nil || len(parts) > replayMaxBlocks || !replayable(parts) {
		return
	}
	canonical, ok := withoutThinking(parts)
	if !ok {
		return
	}
	t.cache.put(t.key(canonical), content)
}

// keepResponse stores the content of a non-streamed Messages reply (see keep).
func (t *replayTurn) keepResponse(raw []byte) {
	if t == nil {
		return
	}
	var r struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &r) == nil && len(r.Content) > 0 {
		t.keep(r.Content)
	}
}

// rejected forgets the content put back into a request Kimi refused as invalid (400 or 422), as
// today's core does, so the next request goes without it.
func (t *replayTurn) rejected(status int) {
	if status == 400 || status == 422 {
		t.forget()
	}
}

// forget drops the content put back into this request, if any: Kimi refused the request, with
// an error status or an error event in its stream.
func (t *replayTurn) forget() {
	if t != nil && t.restored != nil {
		t.cache.drop(t.used, t.restored)
	}
}

func partType(p any) string {
	m, _ := p.(map[string]any)
	s, _ := m["type"].(string)
	return strings.TrimSpace(s)
}

func partString(p any, field string) string {
	m, _ := p.(map[string]any)
	s, _ := m[field].(string)
	return strings.TrimSpace(s)
}

func hasThinking(content []any) bool {
	return slices.ContainsFunc(content, func(p any) bool {
		t := partType(p)
		return t == "thinking" || t == "redacted_thinking"
	})
}

// replayable reports whether content has a signed thinking block and a tool call with an ID.
func replayable(content []any) bool {
	signed := slices.ContainsFunc(content, func(p any) bool { return partType(p) == "thinking" && partString(p, "signature") != "" })
	return signed && slices.ContainsFunc(content, func(p any) bool { return partType(p) == "tool_use" && partString(p, "id") != "" })
}

// withoutThinking returns the canonical JSON of content without its thinking blocks (object keys
// sorted), and whether that has a tool call and every tool call has an ID.
func withoutThinking(content []any) ([]byte, bool) {
	rest := make([]any, 0, len(content))
	tool := false
	for _, p := range content {
		switch partType(p) {
		case "thinking", "redacted_thinking":
			continue
		case "tool_use":
			if partString(p, "id") == "" {
				return nil, false
			}
			tool = true
		}
		rest = append(rest, p)
	}
	if !tool {
		return nil, false
	}
	b, err := json.Marshal(rest)
	return b, err == nil
}

func decodeArray(b []byte) ([]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var a []any
	if err := dec.Decode(&a); err != nil {
		return nil, err
	}
	return a, nil
}

// replayCapture rebuilds a streamed Messages reply's content array from its events, as the
// reply passes to the client, for replayTurn.keep. It gives up on anything it cannot rebuild
// exactly, and beyond the size bounds.
type replayCapture struct {
	blocks    map[int]*capturedBlock
	started   bool
	complete  bool
	failed    bool // the stream had an error event
	abandoned bool
	kept      bool // the content was handed to the turn (replayDecoder)
	size      int
}

type capturedBlock struct {
	raw                       map[string]any
	text, thinking, signature *strings.Builder
	input                     *strings.Builder
	done                      bool
}

func newReplayCapture() *replayCapture { return &replayCapture{blocks: map[int]*capturedBlock{}} }

// observe reads one event's data.
func (c *replayCapture) observe(data []byte) {
	if c.abandoned && c.failed {
		return
	}
	var ev struct {
		Type         string          `json:"type"`
		Index        int             `json:"index"`
		ContentBlock json.RawMessage `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if json.Unmarshal(data, &ev) != nil {
		c.abandon()
		return
	}
	switch ev.Type {
	case "message_start":
		c.started = true
	case "message_stop":
		c.complete = true
	case "error":
		c.failed = true
		c.abandon()
	case "content_block_start":
		if c.abandoned {
			return
		}
		raw, err := decodeObject(ev.ContentBlock)
		if err != nil || raw == nil || c.blocks[ev.Index] != nil || len(c.blocks) >= replayMaxBlocks || !c.reserve(len(ev.ContentBlock)) {
			c.abandon()
			return
		}
		c.blocks[ev.Index] = &capturedBlock{raw: raw}
	case "content_block_delta":
		b := c.blocks[ev.Index]
		if c.abandoned {
			return
		}
		if b == nil {
			c.abandon()
			return
		}
		switch ev.Delta.Type {
		case "text_delta":
			c.append(&b.text, b.raw, "text", ev.Delta.Text)
		case "thinking_delta":
			c.append(&b.thinking, b.raw, "thinking", ev.Delta.Thinking)
		case "signature_delta":
			c.append(&b.signature, b.raw, "signature", ev.Delta.Signature)
		case "input_json_delta":
			if b.input == nil {
				b.input = &strings.Builder{}
			}
			if c.reserve(len(ev.Delta.PartialJSON)) {
				b.input.WriteString(ev.Delta.PartialJSON)
			}
		default:
			c.abandon()
		}
	case "content_block_stop":
		if c.abandoned {
			return
		}
		b := c.blocks[ev.Index]
		if b == nil || (b.input != nil && !json.Valid([]byte(b.input.String()))) {
			c.abandon()
			return
		}
		b.done = true
	}
}

// append adds a delta to one of a block's text fields, which starts from the field's value in
// the block's start event.
func (c *replayCapture) append(sb **strings.Builder, raw map[string]any, field, delta string) {
	if *sb == nil {
		initial, _ := raw[field].(string)
		if !c.reserve(len(initial)) {
			return
		}
		*sb = &strings.Builder{}
		(*sb).WriteString(initial)
	}
	if c.reserve(len(delta)) {
		(*sb).WriteString(delta)
	}
}

func (c *replayCapture) reserve(n int) bool {
	if c.size > replayMaxEntryBytes-n {
		c.abandon()
		return false
	}
	c.size += n
	return true
}

func (c *replayCapture) abandon() {
	c.abandoned = true
	c.blocks = nil
	c.size = 0
}

// content returns the reply's content array, when the stream completed and every block was
// rebuilt.
func (c *replayCapture) content() ([]byte, bool) {
	if !c.started || !c.complete || c.failed || c.abandoned {
		return nil, false
	}
	indexes := make([]int, 0, len(c.blocks))
	for i := range c.blocks {
		indexes = append(indexes, i)
	}
	slices.Sort(indexes)
	parts := make([]any, 0, len(indexes))
	for _, i := range indexes {
		b := c.blocks[i]
		if !b.done {
			return nil, false
		}
		for field, sb := range map[string]*strings.Builder{"text": b.text, "thinking": b.thinking, "signature": b.signature} {
			if sb != nil {
				b.raw[field] = sb.String()
			}
		}
		if b.input != nil {
			b.raw["input"] = json.RawMessage(b.input.String())
		}
		parts = append(parts, b.raw)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(parts) != nil || buf.Len() > replayMaxEntryBytes {
		return nil, false
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), true
}

// replayDecoder passes a Messages stream's events to a capture on their way to the decoder, and
// keeps the turn as soon as the capture is complete, before the decoder turns the stream's last
// event into the finish that ends the reply: a client may send its next turn the moment it has the
// reply, and that turn must find what was kept.
type replayDecoder struct {
	streamDecoder
	capture *replayCapture
	turn    *replayTurn
}

func (d replayDecoder) Decode(data []byte) ([]llm.Event, error) {
	d.capture.observe(data)
	if !d.capture.kept {
		if content, ok := d.capture.content(); ok {
			d.capture.kept = true
			d.turn.keep(content)
		}
	}
	return d.streamDecoder.Decode(data)
}
