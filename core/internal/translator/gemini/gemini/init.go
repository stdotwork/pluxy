package gemini

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

// Register a no-op response translator and a request normalizer for Gemini→Gemini.
// The request converter ensures missing or invalid roles are normalized to valid values.
func init() {
	translator.Register(
		GEMINI,
		GEMINI,
		ConvertGeminiRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    PassthroughGeminiResponseStream,
			NonStream: PassthroughGeminiResponseNonStream,
		},
	)
}
