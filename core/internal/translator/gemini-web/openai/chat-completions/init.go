package chat_completions

import (
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	geminiChat "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/chat-completions"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator/translator"
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
