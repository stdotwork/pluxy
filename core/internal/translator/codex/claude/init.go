package claude

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		CLAUDE,
		CODEX,
		ConvertClaudeRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToClaude,
			NonStream: ConvertCodexResponseToClaudeNonStream,
		},
	)
}
