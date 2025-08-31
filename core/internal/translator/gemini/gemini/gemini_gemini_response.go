package gemini

import (
	"context"
)

// PassthroughGeminiResponseStream forwards Gemini responses unchanged.
func PassthroughGeminiResponseStream(_ context.Context, _ string, rawJSON []byte, _ *any) []string {
	return []string{string(rawJSON)}
}

// PassthroughGeminiResponseNonStream forwards Gemini responses unchanged.
func PassthroughGeminiResponseNonStream(_ context.Context, _ string, rawJSON []byte, _ *any) string {
	return string(rawJSON)
}
