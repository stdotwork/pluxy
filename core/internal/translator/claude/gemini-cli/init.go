package geminiCLI

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		GEMINICLI,
		CLAUDE,
		ConvertGeminiCLIRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToGeminiCLI,
			NonStream: ConvertClaudeResponseToGeminiCLINonStream,
		},
	)
}
