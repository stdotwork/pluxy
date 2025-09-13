package claude

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
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
