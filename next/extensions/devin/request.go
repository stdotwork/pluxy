package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
)

// A chat request is one GetChatMessageRequest message, built as Devin's own CLI builds it and
// today's core sends it. Its fields, by number:
//
//	1  client metadata: the CLI's name and version, the session token, locale, OS and a
//	   device fingerprint
//	2  system prompt
//	3  conversation turns, each: 1 ID, 2 source (1 user, 2 assistant, 4 tool result),
//	   3 text, 6 tool calls (1 ID, 2 name, 3 arguments), 7 the call a result answers,
//	   10 images (1 base64 data, 2 media type), 11 reasoning text, 12 reasoning signature,
//	   18 its type
//	7  5
//	8  completion settings: 2 output token limit, 5 temperature, and fixed values
//	10 tools: 1 name, 2 description, 3 JSON Schema of the parameters
//	15 session: 1 session ID, 3 4, and 4 14 when the last turn is the user's
//	16 cascade ID (the session ID)
//	20 1
//	21 model ID (models.go)
//
// The fixed values are the CLI's, with today's core's meaning unknown.

const (
	// cliName and cliVersion identify Devin's CLI, as today's core names it.
	cliName    = "chisel"
	cliVersion = "3000.10.21"
	// maxOutputTokens is the output limit every model in today's Devin catalog has; a request
	// gets it when it names none or more.
	maxOutputTokens = 64000
	// fingerprintLen is the length of the device fingerprint, in hex digits.
	fingerprintLen = 732
)

// Turn sources.
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

// chatRequest is what a request becomes before it is written.
type chatRequest struct {
	system      string
	turns       []turn
	tools       []llm.Tool
	maxTokens   int
	temperature float64
	model       string
	losses      []llm.Loss
}

// buildRequest turns a request into a chat request, listing what Devin has no place for.
func buildRequest(req *llm.Request) *chatRequest {
	c := &chatRequest{model: modelID(req.Model, req.Options.ReasoningEffort), maxTokens: maxOutputTokens, temperature: 1}
	lose := func(kind, path, detail string) {
		c.losses = llm.AddLoss(c.losses, llm.Loss{Kind: kind, Path: path, Detail: detail})
	}
	o := req.Options
	if o.MaxOutputTokens != nil && *o.MaxOutputTokens > 0 && *o.MaxOutputTokens < maxOutputTokens {
		c.maxTokens = *o.MaxOutputTokens
	}
	if o.Temperature != nil {
		c.temperature = *o.Temperature
	}
	if o.TopP != nil {
		lose(llm.LossDropped, "top_p", "Devin takes no top_p")
	}
	if len(o.Stop) > 0 {
		lose(llm.LossDropped, "stop", "Devin takes no stop sequences")
	}
	if o.ToolChoice != nil && o.ToolChoice.Mode != llm.ToolChoiceAuto {
		lose(llm.LossDropped, "tool_choice", "Devin takes no tool choice")
	}
	if o.ResponseFormat != nil && o.ResponseFormat.Type != llm.ResponseFormatText {
		lose(llm.LossDropped, "response_format", "Devin takes no response format")
	}
	for _, t := range req.Tools {
		if t.Type != "" && t.Type != llm.ToolFunction || t.Name == "" {
			lose(llm.LossDropped, originPath(t.Origin, "tools"), "Devin takes only function tools")
			continue
		}
		c.tools = append(c.tools, t)
	}

	var system []string
	pending := map[string]bool{} // calls a tool result may answer
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
			text, images := content(it, path, lose)
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
				c.turns = append(c.turns, turn{source: sourceUser, content: labelImages(text, images), images: images})
			}
		case llm.ItemTypeReasoning:
			t := last()
			if t == nil || t.source != sourceAssistant || t.thinking != "" || t.content != "" || len(t.calls) > 0 {
				c.turns = append(c.turns, turn{source: sourceAssistant})
				t = last()
			}
			t.thinking = it.Text
			t.signature = signatureOf(it.Origin)
		case llm.ItemTypeToolCall:
			t := assistant()
			t.calls = append(t.calls, toolCall{id: it.CallID, name: it.Name, args: it.Arguments})
			if it.CallID != "" {
				pending[it.CallID] = true
			}
		case llm.ItemTypeToolResult:
			if pending[it.CallID] {
				c.turns = append(c.turns, turn{source: sourceTool, content: it.Output, callID: it.CallID})
			} else {
				// A result for no call in the conversation goes as the user's text, as today's
				// core sends it.
				c.turns = append(c.turns, turn{source: sourceUser, content: it.Output})
			}
		default:
			lose(llm.LossDropped, path, "Devin has no place for a "+it.Type+" item")
		}
	}
	c.system = strings.Join(system, "\n")
	return c
}

// content returns a message's text, its parts joined by a line break, and its images. Devin
// takes images as base64 data, so an image given as a link and other parts are dropped.
func content(it llm.Item, path string, lose func(kind, path, detail string)) (string, []image) {
	var texts []string
	var images []image
	for j, p := range it.Content {
		ppath := originPath(p.Origin, fmt.Sprintf("%s.content[%d]", path, j))
		switch p.Type {
		case llm.PartText:
			texts = append(texts, p.Text)
		case llm.PartImage:
			if img, ok := dataImage(p.URL); ok && it.Role == llm.RoleUser {
				images = append(images, img)
				continue
			}
			lose(llm.LossDropped, ppath, "Devin takes images only as base64 data in a user turn")
		default:
			lose(llm.LossDropped, ppath, "Devin has no place for a "+p.Type+" part")
		}
	}
	return strings.Join(texts, "\n"), images
}

// labelImages puts a line naming each image before a user turn's text, as Devin's CLI names
// pasted images and today's core sends them: "[Image 1: pasted_image_1.png]".
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

// dataImage reads an image given as a data: URL.
func dataImage(u string) (image, bool) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return image{}, false
	}
	meta, data, ok := strings.Cut(rest, ",")
	mediaType, isBase64 := strings.CutSuffix(meta, ";base64")
	if !ok || !isBase64 || strings.TrimSpace(data) == "" {
		return image{}, false
	}
	if mediaType == "" {
		mediaType = "image/png"
	}
	return image{data: data, mediaType: mediaType}, true
}

// signatureOf returns the reasoning signature a client replayed, as a reply gave it: a thinking
// block's signature or a Responses reasoning item's encrypted_content. Devin's signatures are
// text, sent as they are.
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

// encode writes the chat request as a GetChatMessageRequest message.
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

// deviceFingerprint returns the device fingerprint the CLI sends: from a seed, the SHA-256 of
// "seed-0", "seed-1", ... in hex, joined and cut to length; without one, random hex, new for
// each request, as today's core sends it.
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

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
