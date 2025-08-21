package gemini

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
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
