package responses

import "context"

func ConvertGeminiResponseToOpenAIResponses(_ context.Context, modelName string, rawJSON []byte, param *any) []string {
	return nil
}

func ConvertGeminiResponseToOpenAIResponsesNonStream(_ context.Context, _ string, rawJSON []byte, _ *any) string {
	return ""
}
