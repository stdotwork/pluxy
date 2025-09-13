package chat_completions

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		OPENAI,
		GEMINICLI,
		ConvertOpenAIRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertCliResponseToOpenAI,
			NonStream: ConvertCliResponseToOpenAINonStream,
		},
	)
}
