package responses

import "context"

func ConvertGeminiCLIResponseToOpenAIResponses(_ context.Context, modelName string, rawJSON []byte, param *any) []string {
	return nil
}

func ConvertGeminiCLIResponseToOpenAIResponsesNonStream(_ context.Context, _ string, rawJSON []byte, _ *any) string {
	return ""
}
