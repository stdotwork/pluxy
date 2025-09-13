package geminiCLI

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		GEMINICLI,
		GEMINI,
		ConvertGeminiCLIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToGeminiCLI,
			NonStream: ConvertGeminiResponseToGeminiCLINonStream,
		},
	)
}
