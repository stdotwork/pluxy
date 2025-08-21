package claude

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		CLAUDE,
		GEMINICLI,
		ConvertClaudeRequestToCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiCLIResponseToClaude,
			NonStream: ConvertGeminiCLIResponseToClaudeNonStream,
		},
	)
}
