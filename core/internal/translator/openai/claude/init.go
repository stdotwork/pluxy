package claude

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		CLAUDE,
		OPENAI,
		ConvertClaudeRequestToOpenAI,
		interfaces.TranslateResponse{
			Stream:    ConvertOpenAIResponseToClaude,
			NonStream: ConvertOpenAIResponseToClaudeNonStream,
		},
	)
}
