package gemini

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
)

func init() {
	translator.Register(
		GEMINI,
		CODEX,
		ConvertGeminiRequestToCodex,
		interfaces.TranslateResponse{
			Stream:    ConvertCodexResponseToGemini,
			NonStream: ConvertCodexResponseToGeminiNonStream,
		},
	)
}
