package geminiwebapi

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

const continuationHint = "\n(More messages to come, please reply with just 'ok.')"

func ChunkByRunes(s string, size int) []string {
	if size <= 0 {
		return []string{s}
	}
	chunks := make([]string, 0, (len(s)/size)+1)
	var buf strings.Builder
	count := 0
	for _, r := range s {
		buf.WriteRune(r)
		count++
		if count >= size {
			chunks = append(chunks, buf.String())
			buf.Reset()
			count = 0
		}
	}
	if buf.Len() > 0 {
		chunks = append(chunks, buf.String())
	}
	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

func MaxCharsPerRequest(cfg *config.Config) int {
	// Read max characters per request from config with a conservative default.
	if cfg != nil {
		if v := cfg.GeminiWeb.MaxCharsPerRequest; v > 0 {
			return v
		}
	}
	return 1_000_000
}

func SendWithSplit(chat *ChatSession, text string, files []string, cfg *config.Config) (ModelOutput, error) {
	// Validate chat session
	if chat == nil {
		return ModelOutput{}, fmt.Errorf("nil chat session")
	}

	// Resolve maxChars characters per request
	maxChars := MaxCharsPerRequest(cfg)
	if maxChars <= 0 {
		maxChars = 1_000_000
	}

	// If within limit, send directly
	if utf8.RuneCountInString(text) <= maxChars {
		return chat.SendMessage(text, files)
	}

	// Decide whether to use continuation hint (enabled by default)
	useHint := true
	if cfg != nil && cfg.GeminiWeb.DisableContinuationHint {
		useHint = false
	}

	// Compute chunk size in runes. If the hint does not fit, disable it for this request.
	hintLen := 0
	if useHint {
		hintLen = utf8.RuneCountInString(continuationHint)
	}
	chunkSize := maxChars - hintLen
	if chunkSize <= 0 {
		// maxChars is too small to accommodate the hint; fall back to no-hint splitting
		useHint = false
		chunkSize = maxChars
	}

	// Split into rune-safe chunks
	chunks := ChunkByRunes(text, chunkSize)
	if len(chunks) == 0 {
		chunks = []string{""}
	}

	// Send all but the last chunk without files, optionally appending hint
	for i := 0; i < len(chunks)-1; i++ {
		part := chunks[i]
		if useHint {
			part += continuationHint
		}
		if _, err := chat.SendMessage(part, nil); err != nil {
			return ModelOutput{}, err
		}
	}

	// Send final chunk with files and return the actual output
	return chat.SendMessage(chunks[len(chunks)-1], files)
}
