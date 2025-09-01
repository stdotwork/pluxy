package responses

import "context"

func ConvertOpenAIChatCompletionsResponseToOpenAIResponses(_ context.Context, modelName string, rawJSON []byte, param *any) []string {
	return nil
}

func ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(_ context.Context, _ string, rawJSON []byte, _ *any) string {
	return ""
}
