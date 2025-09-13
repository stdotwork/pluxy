package gemini

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
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
