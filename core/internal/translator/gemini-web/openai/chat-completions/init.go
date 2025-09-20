package chat_completions

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	geminiChat "github.com/luispater/CLIProxyAPI/v5/internal/translator/gemini/openai/chat-completions"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		OPENAI,
		GEMINIWEB,
		geminiChat.ConvertOpenAIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    geminiChat.ConvertGeminiResponseToOpenAI,
			NonStream: geminiChat.ConvertGeminiResponseToOpenAINonStream,
		},
	)
}
