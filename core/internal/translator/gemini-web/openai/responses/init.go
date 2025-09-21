package responses

import (
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	geminiResponses "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
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
