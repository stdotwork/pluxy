package responses

import (
	"bytes"

	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini-cli/gemini"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/gemini/openai/responses"
	log "github.com/sirupsen/logrus"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, inputRawJSON []byte, stream bool) []byte {
	log.Debug("ConvertOpenAIResponsesRequestToGeminiCLI")
	rawJSON := bytes.Clone(inputRawJSON)
	rawJSON = ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
	return ConvertGeminiRequestToGeminiCLI(modelName, rawJSON, stream)
}
