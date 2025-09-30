package conversation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Message represents a minimal role-text pair used for hashing and comparison.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// StoredMessage mirrors the persisted conversation message structure.
type StoredMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// Sha256Hex computes SHA-256 hex digest for the specified string.
func Sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ToStoredMessages converts in-memory messages into the persisted representation.
func ToStoredMessages(msgs []Message) []StoredMessage {
	out := make([]StoredMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, StoredMessage{Role: m.Role, Content: m.Text})
	}
	return out
}

// StoredToMessages converts stored messages back into the in-memory representation.
func StoredToMessages(msgs []StoredMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Message{Role: m.Role, Text: m.Content})
	}
	return out
}

// hashMessage normalizes message data and returns a stable digest.
func hashMessage(m StoredMessage) string {
	s := fmt.Sprintf(`{"content":%q,"role":%q}`, m.Content, strings.ToLower(m.Role))
	return Sha256Hex(s)
}

// HashConversationWithPrefix computes a conversation hash using the provided prefix (client identifier) and model.
func HashConversationWithPrefix(prefix, model string, msgs []StoredMessage) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(prefix)))
	b.WriteString("|")
	b.WriteString(strings.ToLower(strings.TrimSpace(model)))
	for _, m := range msgs {
		b.WriteString("|")
		b.WriteString(hashMessage(m))
	}
	return Sha256Hex(b.String())
}

// HashConversationForAccount keeps compatibility with the per-account hash previously used.
func HashConversationForAccount(clientID, model string, msgs []StoredMessage) string {
	return HashConversationWithPrefix(clientID, model, msgs)
}

// HashConversationGlobal produces a hash suitable for cross-account lookups.
func HashConversationGlobal(model string, msgs []StoredMessage) string {
	return HashConversationWithPrefix("global", model, msgs)
}
