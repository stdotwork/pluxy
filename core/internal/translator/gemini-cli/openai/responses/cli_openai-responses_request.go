package responses

import (
	. "github.com/luispater/CLIProxyAPI/internal/translator/gemini/openai/responses"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToGeminiCLI(modelName string, rawJSON []byte, stream bool) []byte {
	modelResult := gjson.GetBytes(rawJSON, "model")
	rawJSON = []byte(gjson.GetBytes(rawJSON, "request").Raw)
	rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelResult.String())
	if gjson.GetBytes(rawJSON, "systemInstruction").Exists() {
		rawJSON, _ = sjson.SetRawBytes(rawJSON, "system_instruction", []byte(gjson.GetBytes(rawJSON, "systemInstruction").Raw))
		rawJSON, _ = sjson.DeleteBytes(rawJSON, "systemInstruction")
	}

	return ConvertOpenAIResponsesRequestToGemini(modelName, rawJSON, stream)
}
