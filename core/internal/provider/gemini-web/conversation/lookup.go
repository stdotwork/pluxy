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
	if len(sanitized) == 0 {
		return nil
	}
	result := make([]PrefixHash, 0, len(sanitized))
	seen := make(map[string]struct{}, len(sanitized))
	for start := 0; start < len(sanitized); start++ {
		segment := sanitized[start:]
		if len(segment) < 2 {
			continue
		}
		tailRole := strings.ToLower(strings.TrimSpace(segment[len(segment)-1].Role))
		if tailRole != "assistant" && tailRole != "system" {
			continue
		}
		hash := HashConversationGlobal(model, ToStoredMessages(segment))
		if _, exists := seen[hash]; exists {
			continue
		}
		seen[hash] = struct{}{}
		result = append(result, PrefixHash{Hash: hash, PrefixLen: len(segment)})
	}
	if len(result) == 0 {
		hash := HashConversationGlobal(model, ToStoredMessages(sanitized))
		return []PrefixHash{{Hash: hash, PrefixLen: len(sanitized)}}
	}
	return result
}
