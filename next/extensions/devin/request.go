package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

const (
	cliName         = "chisel"
	cliVersion      = "3000.10.21"
	maxOutputTokens = 64000
	fingerprintLen  = 732
)

const (
	sourceUser      = 1
	sourceAssistant = 2
	sourceTool      = 4
)

type toolCall struct{ id, name, args string }

type image struct{ data, mediaType string }

type turn struct {
	source    int
	content   string
	emptied   bool
	dropped   string
	calls     []toolCall
	callID    string
	images    []image
	thinking  string
	signature []byte
}

func (t turn) encode() []byte {
	m := message{}.str(1, newUUID()).varint(2, uint64(t.source)).str(3, t.content)
	for _, c := range t.calls {
		var cm message
		if c.id != "" {
			cm = cm.str(1, c.id)
		}
		if c.name != "" {
			cm = cm.str(2, c.name)
		}
		if c.args != "" {
			cm = cm.str(3, c.args)
		}
		m = m.bytes(6, cm)
	}
	if t.callID != "" {
		m = m.str(7, t.callID)
	}
	for _, img := range t.images {
		m = m.bytes(10, message{}.str(1, img.data).str(2, img.mediaType))
	}
	if t.thinking != "" {
		m = m.str(11, t.thinking)
	}
	if len(t.signature) > 0 {
		m = m.bytes(12, t.signature)
		if bytes.HasPrefix(t.signature, []byte("sealed.v1.")) {
			m = m.str(18, "sealed")
		}
	}
	return m
}

type chatRequest struct {
	system      string
	turns       []turn
	tools       []llm.Tool
	maxTokens   int
	temperature float64
	model       string
	losses      []llm.Loss
}

func (c *chatRequest) lose(kind, path, detail string) {
	c.losses = llm.AddLoss(c.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
}

func (c *chatRequest) kept(o *llm.Origin, used ...string) {
	if o == nil || len(o.Raw) == 0 {
		return
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(o.Raw, &m) != nil {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if !slices.Contains(used, k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		c.lose(llm.LossDropped, joinPath(o.Path, k), "Devin has no place for it")
	}
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func buildRequest(req *llm.Request) (*chatRequest, error) {
	c := &chatRequest{model: modelID(req.Model, req.Options.ReasoningEffort), maxTokens: maxOutputTokens, temperature: 1}
	lose := c.lose
	c.kept(req.Origin)
	o := req.Options
	if o.MaxOutputTokens != nil && *o.MaxOutputTokens > 0 && *o.MaxOutputTokens < maxOutputTokens {
		c.maxTokens = *o.MaxOutputTokens
	}
	if o.Temperature != nil {
		c.temperature = *o.Temperature
	}
	if o.CandidateCount > 1 {
		lose(llm.LossDropped, llm.CandidateCountPath(*req), "Devin returns one candidate")
	}
	if o.TopP != nil {
		lose(llm.LossDropped, "top_p", "Devin takes no top_p")
	}
	if len(o.Stop) > 0 {
		lose(llm.LossDropped, "stop", "Devin takes no stop sequences")
	}
	if tc := o.ToolChoice; tc != nil {
		switch {
		case tc.Mode != llm.ToolChoiceAuto:
			lose(llm.LossDropped, "tool_choice", "Devin takes no tool choice")
		case len(tc.Allowed) > 0:
			lose(llm.LossDropped, "tool_choice", "Devin cannot limit a request to an allowed tool list")
		}
	}
	if o.ResponseFormat != nil && o.ResponseFormat.Type != llm.ResponseFormatText {
		lose(llm.LossDropped, "response_format", "Devin takes no response format")
	}
	if o.ParallelToolCalls != nil && !*o.ParallelToolCalls {
		lose(llm.LossDropped, "parallel_tool_calls", "Devin may call several tools at once")
	}
	for i, t := range req.Tools {
		path := originPath(t.Origin, fmt.Sprintf("tools[%d]", i))
		if t.Type != "" && t.Type != llm.ToolFunction || t.Name == "" {
			lose(llm.LossDropped, path, "Devin takes only function tools")
			continue
		}
		if t.Strict != nil && *t.Strict {
			lose(llm.LossDropped, joinPath(path, "strict"), "Devin has no strict tool arguments")
		}
		c.kept(t.Origin)
		c.tools = append(c.tools, t)
	}

	var system []string
	pending := map[string]bool{}
	last := func() *turn {
		if len(c.turns) == 0 {
			return nil
		}
		return &c.turns[len(c.turns)-1]
	}
	assistant := func() *turn {
		if t := last(); t != nil && t.source == sourceAssistant {
			return t
		}
		c.turns = append(c.turns, turn{source: sourceAssistant})
		return last()
	}
	for i, it := range req.Items {
		path := originPath(it.Origin, fmt.Sprintf("items[%d]", i))
		switch it.Type {
		case llm.ItemTypeMessage:
			c.kept(it.Origin)
			text, images, dropped := c.content(it, path)
			switch it.Role {
			case llm.RoleSystem, llm.RoleDeveloper:
				if text != "" {
					system = append(system, text)
				}
			case llm.RoleAssistant:
				t := assistant()
				if t.content != "" && text != "" {
					t.content += "\n"
				}
				t.content += text
			default:
				t := turn{source: sourceUser, content: labelImages(text, images), images: images, dropped: dropped}
				t.emptied = dropped != "" && strings.TrimSpace(t.content) == "" && len(images) == 0
				c.turns = append(c.turns, t)
			}
		case llm.ItemTypeReasoning:
			t := last()
			if t == nil || t.source != sourceAssistant || t.thinking != "" || t.content != "" || len(t.calls) > 0 {
				c.turns = append(c.turns, turn{source: sourceAssistant})
				t = last()
			}
			t.thinking = it.Text
			t.signature = signatureOf(it.Origin)
			c.kept(it.Origin, "signature", "encrypted_content")
		case llm.ItemTypeToolCall:
			c.kept(it.Origin)
			t := assistant()
			t.calls = append(t.calls, toolCall{id: it.CallID, name: it.Name, args: it.Arguments})
			if it.CallID != "" {
				pending[it.CallID] = true
			}
		case llm.ItemTypeToolResult:
			out := c.resultText(it)
			if pending[it.CallID] {
				c.turns = append(c.turns, turn{source: sourceTool, content: out, callID: it.CallID})
			} else {
				c.turns = append(c.turns, turn{source: sourceUser, content: out})
			}
		default:
			lose(llm.LossDropped, path, "Devin has no place for a "+it.Type+" item")
		}
	}
	c.system = strings.Join(system, "\n")
	if err := c.dropEmptied(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *chatRequest) dropEmptied() error {
	start := 0
	for i := 0; i <= len(c.turns); i++ {
		if i < len(c.turns) && c.turns[i].source != sourceAssistant {
			continue
		}
		dropped, alive := "", false
		for _, t := range c.turns[start:i] {
			if t.emptied && dropped == "" {
				dropped = t.dropped
			}
			alive = alive || !t.emptied
		}
		if dropped != "" && !alive {
			return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeRequest,
				Message: "Devin cannot take the " + dropped + " content of a user turn, and the turn has nothing else"}
		}
		start = i + 1
	}
	c.turns = slices.DeleteFunc(c.turns, func(t turn) bool { return t.emptied })
	return nil
}

func (c *chatRequest) content(it llm.Item, path string) (string, []image, string) {
	var texts []string
	var images []image
	var dropped string
	drop := func(typ, ppath, detail string) {
		c.lose(llm.LossDropped, ppath, detail)
		if dropped == "" {
			dropped = typ
		}
	}
	for j, p := range it.Content {
		ppath := originPath(p.Origin, fmt.Sprintf("%s.content[%d]", path, j))
		c.kept(p.Origin)
		switch p.Type {
		case llm.PartText:
			texts = append(texts, p.Text)
		case llm.PartImage:
			if img, ok := dataImage(p.URL); ok && it.Role == llm.RoleUser {
				images = append(images, img)
				continue
			}
			drop(p.Type, ppath, "Devin takes images only as base64 data in a user turn")
		default:
			drop(p.Type, ppath, "Devin has no place for a "+p.Type+" part")
		}
	}
	return strings.Join(texts, "\n"), images, dropped
}

func (c *chatRequest) resultText(it llm.Item) string {
	itemPath := ""
	if it.Origin != nil {
		itemPath = it.Origin.Path
	}
	if it.IsError {
		c.lose(llm.LossDropped, joinPath(itemPath, "is_error"), "Devin has no error flag on a tool result")
	}
	if len(it.Content) > 0 {
		for i, p := range it.Content {
			if p.Type == llm.PartText {
				c.kept(p.Origin)
				continue
			}
			pp := fmt.Sprintf("%s[%d]", joinPath(itemPath, "content"), i)
			if p.Origin != nil && p.Origin.Path != "" {
				pp = p.Origin.Path
			}
			c.lose(llm.LossDropped, pp, "Devin takes only text in a tool result")
		}
		c.kept(it.Origin)
		return it.Output
	}
	if it.Output != "" || it.Origin == nil {
		c.kept(it.Origin)
		return it.Output
	}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(it.Origin.Raw, &m)
	for _, key := range []string{"content", "output"} {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(m[key], &blocks) != nil || len(blocks) == 0 {
			continue
		}
		var texts []string
		for j, b := range blocks {
			switch b.Type {
			case "text", "input_text", "output_text":
				texts = append(texts, b.Text)
			default:
				c.lose(llm.LossDropped, fmt.Sprintf("%s[%d]", joinPath(it.Origin.Path, key), j), "Devin takes only text in a tool result")
			}
		}
		c.kept(it.Origin, key)
		return strings.Join(texts, "\n")
	}
	c.kept(it.Origin)
	return ""
}

func labelImages(text string, images []image) string {
	if len(images) == 0 || strings.Contains(text, "[Image ") {
		return text
	}
	labels := make([]string, len(images))
	for i, img := range images {
		ext := "png"
		switch strings.ToLower(img.mediaType) {
		case "image/jpeg", "image/jpg":
			ext = "jpg"
		case "image/webp":
			ext = "webp"
		case "image/gif":
			ext = "gif"
		}
		labels[i] = fmt.Sprintf("[Image %d: pasted_image_%d.%s]", i+1, i+1, ext)
	}
	header := strings.Join(labels, "\n")
	if text == "" {
		return header
	}
	return header + "\n\n" + text
}

func dataImage(u string) (image, bool) {
	const scheme = "data:"
	if len(u) < len(scheme) || !strings.EqualFold(u[:len(scheme)], scheme) {
		return image{}, false
	}
	meta, data, ok := strings.Cut(u[len(scheme):], ",")
	const marker = ";base64"
	if !ok || len(meta) < len(marker) || !strings.EqualFold(meta[len(meta)-len(marker):], marker) || strings.TrimSpace(data) == "" {
		return image{}, false
	}
	mediaType := meta[:len(meta)-len(marker)]
	if mediaType == "" {
		mediaType = "image/png"
	}
	return image{data: data, mediaType: mediaType}, true
}

func signatureOf(o *llm.Origin) []byte {
	if o == nil || len(o.Raw) == 0 {
		return nil
	}
	var raw struct {
		Signature        string `json:"signature"`
		EncryptedContent string `json:"encrypted_content"`
	}
	_ = json.Unmarshal(o.Raw, &raw)
	s := raw.Signature
	if s == "" {
		s = raw.EncryptedContent
	}
	if s == "" {
		return nil
	}
	return []byte(s)
}

func originPath(o *llm.Origin, fallback string) string {
	if o != nil && o.Path != "" {
		return o.Path
	}
	return fallback
}

func (c *chatRequest) encode(token, fingerprint string) []byte {
	session := newUUID()
	meta := message{}.str(1, cliName).str(2, cliVersion).str(3, token).str(4, "en").str(5, runtime.GOOS).
		str(7, cliVersion).str(12, cliName).str(31, fingerprint)
	m := message{}.bytes(1, meta)
	if c.system != "" {
		m = m.str(2, c.system)
	}
	for _, t := range c.turns {
		m = m.bytes(3, t.encode())
	}
	m = m.varint(7, 5)
	// float64(float32(0.95)) is the top-p the CLI sends, a float32 widened.
	settings := message{}.varint(1, 1).varint(2, uint64(c.maxTokens)).varint(3, 400).double(5, c.temperature).
		varint(7, 40).double(8, float64(float32(0.95)))
	m = m.bytes(8, settings)
	for _, t := range c.tools {
		tm := message{}.str(1, t.Name)
		if t.Description != "" {
			tm = tm.str(2, t.Description)
		}
		if len(t.Parameters) > 0 {
			tm = tm.bytes(3, t.Parameters)
		}
		m = m.bytes(10, tm)
	}
	sm := message{}.str(1, session).varint(3, 4)
	if n := len(c.turns); n > 0 && c.turns[n-1].source == sourceUser {
		sm = sm.varint(4, 14)
	}
	m = m.bytes(15, sm)
	return m.str(16, session).varint(20, 1).str(21, c.model)
}

func deviceFingerprint(seed string) string {
	if seed == "" {
		b := make([]byte, fingerprintLen/2)
		_, _ = rand.Read(b)
		return hex.EncodeToString(b)
	}
	var sb strings.Builder
	for i := 0; sb.Len() < fingerprintLen; i++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, i)))
		sb.WriteString(hex.EncodeToString(sum[:]))
	}
	return sb.String()[:fingerprintLen]
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
