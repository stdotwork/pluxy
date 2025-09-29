package conversation

import "strings"

// PrefixHash represents a hash candidate for a specific prefix length.
type PrefixHash struct {
	Hash      string
	PrefixLen int
}

// BuildLookupHashes generates hash candidates ordered from longest to shortest prefix.
func BuildLookupHashes(model string, msgs []Message) []PrefixHash {
	if len(msgs) < 2 {
		return nil
	}
	model = NormalizeModel(model)
	sanitized := SanitizeAssistantMessages(msgs)
	result := make([]PrefixHash, 0, len(sanitized))
	for end := len(sanitized); end >= 2; end-- {
		tailRole := strings.ToLower(strings.TrimSpace(sanitized[end-1].Role))
		if tailRole != "assistant" && tailRole != "system" {
			continue
		}
		prefix := sanitized[:end]
		hash := HashConversationGlobal(model, ToStoredMessages(prefix))
		result = append(result, PrefixHash{Hash: hash, PrefixLen: end})
	}
	return result
}

// BuildStorageHashes returns hashes representing the full conversation snapshot.
func BuildStorageHashes(model string, msgs []Message) []PrefixHash {
	if len(msgs) == 0 {
		return nil
	}
	model = NormalizeModel(model)
	sanitized := SanitizeAssistantMessages(msgs)
	hash := HashConversationGlobal(model, ToStoredMessages(sanitized))
	return []PrefixHash{{Hash: hash, PrefixLen: len(sanitized)}}
}
