package gemini

import (
	. "github.com/luispater/CLIProxyAPI/v5/internal/constant"
	"github.com/luispater/CLIProxyAPI/v5/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/v5/internal/translator/translator"
)

func init() {
	translator.Register(
		GEMINI,
		GEMINICLI,
		ConvertGeminiRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiCliRequestToGemini,
			NonStream: ConvertGeminiCliRequestToGeminiNonStream,
		},
	)
}
