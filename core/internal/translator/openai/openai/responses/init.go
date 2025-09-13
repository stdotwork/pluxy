package responses

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		OPENAI_RESPONSE,
		OPENAI,
		ConvertOpenAIResponsesRequestToOpenAIChatCompletions,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIChatCompletionsResponseToOpenAIResponses,
			NonStream: ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream,
		},
	)
}
