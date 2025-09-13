package gemini

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		GEMINI,
		OPENAI,
		ConvertGeminiRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIResponseToGemini,
			NonStream: ConvertOpenAIResponseToGeminiNonStream,
		},
	)
}
