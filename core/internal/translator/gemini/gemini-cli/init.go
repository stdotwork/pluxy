package geminiCLI

import (
	. "github.com/luispater/CLIProxyAPI/internal/constant"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/translator/translator"
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
