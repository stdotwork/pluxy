package responses

import "context"

func ConvertClaudeResponseToOpenAIResponses(_ context.Context, modelName string, rawJSON []byte, param *any) []string {
	return nil
}

func ConvertClaudeResponseToOpenAIResponsesNonStream(_ context.Context, _ string, rawJSON []byte, _ *any) string {
	return ""
}
