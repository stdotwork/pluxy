package responses

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	geminiResponses "github.com/luispater/CLIProxyAPI/v5/internal/translator/gemini/openai/responses"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		OPENAI_RESPONSE,
		GEMINIWEB,
		geminiResponses.ConvertOpenAIResponsesRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    geminiResponses.ConvertGeminiResponseToOpenAIResponses,
			NonStream: geminiResponses.ConvertGeminiResponseToOpenAIResponsesNonStream,
		},
	)
}
