package openairesponses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Encrypted reasoning carried through another format. A Responses reasoning item's
// encrypted_content is opaque: only the upstream that made it can read it. A client in a format
// with its own opaque reasoning field (an Anthropic Messages thinking block's signature) can hold
// it there and replay it, so the reasoning survives a turn through that client.

// maxEncryptedReasoning bounds the encrypted content this package carries between formats.
const maxEncryptedReasoning = 32 << 20

// IsEncryptedReasoning reports whether s has the shape of the encrypted content OpenAI puts on
// reasoning items: a Fernet token (version byte 0x80, 8-byte timestamp, 16-byte IV, ciphertext in
// whole AES blocks, 32-byte HMAC) in base64url, optionally followed by a dot and a short
// alphanumeric tag that some routers append. It checks the shape only, never that the token
// decrypts; it tells an OpenAI reasoning token from another provider's opaque data, such as an
// Anthropic thinking signature, which is standard base64 of something else.
func IsEncryptedReasoning(s string) bool {
	if len(s) > maxEncryptedReasoning || !strings.HasPrefix(s, "gAAAA") {
		return false
	}
	if head, tag, ok := strings.Cut(s, "."); ok {
		if !routerTag(tag) {
			return false
		}
		s = head
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil || len(b) < 1+8+16+16+32 || b[0] != 0x80 {
		return false
	}
	return (len(b)-1-8-16-32)%16 == 0
}

func routerTag(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// ReasoningSignature returns the encrypted content of the reasoning item in raw, an upstream
// response.output_item.done event or an output item, when it has the shape IsEncryptedReasoning
// checks; otherwise "".
func ReasoningSignature(raw json.RawMessage) string {
	var ev struct {
		Type string          `json:"type"`
		Item json.RawMessage `json:"item"`
	}
	if json.Unmarshal(raw, &ev) != nil {
		return ""
	}
	item := raw
	if ev.Type == "response.output_item.done" {
		item = ev.Item
	}
	var it struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal(item, &it) != nil || it.Type != "reasoning" || !IsEncryptedReasoning(it.EncryptedContent) {
		return ""
	}
	return it.EncryptedContent
}

// ReasoningSignatures returns what ReasoningSignature returns for each reasoning item in raw (an
// output_item.done event, a terminal event or a Response object) that has one, by the path
// OpaqueOutput reports its encrypted content under.
func ReasoningSignatures(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	outputItems(raw, func(i int, item json.RawMessage) {
		if s := ReasoningSignature(item); s != "" {
			out[fmt.Sprintf("output[%d].encrypted_content", i)] = s
		}
	})
	return out
}
