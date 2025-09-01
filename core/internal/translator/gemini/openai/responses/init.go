package responses

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		OPENAI_RESPONSE,
		GEMINI,
		ConvertOpenAIResponsesRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToOpenAIResponses,
			NonStream: ConvertGeminiResponseToOpenAIResponsesNonStream,
		},
	)
}
