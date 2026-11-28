package openairesponses

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const maxEncryptedReasoning = 32 << 20

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

func reasoningSignature(item json.RawMessage) string {
	var it struct {
		Type             string `json:"type"`
		EncryptedContent string `json:"encrypted_content"`
	}
	if json.Unmarshal(item, &it) != nil || it.Type != "reasoning" || !IsEncryptedReasoning(it.EncryptedContent) {
		return ""
	}
	return it.EncryptedContent
}

func ReasoningSignatures(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	outputItems(raw, func(i int, item json.RawMessage) {
		if s := reasoningSignature(item); s != "" {
			out[fmt.Sprintf("output[%d].encrypted_content", i)] = s
		}
	})
	return out
}
