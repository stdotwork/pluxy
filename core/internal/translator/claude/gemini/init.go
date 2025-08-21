package gemini

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		GEMINI,
		CLAUDE,
		ConvertGeminiRequestToClaude,
		interfaces.TranslateResponse{
			Stream:    ConvertClaudeResponseToGemini,
			NonStream: ConvertClaudeResponseToGeminiNonStream,
		},
	)
}
