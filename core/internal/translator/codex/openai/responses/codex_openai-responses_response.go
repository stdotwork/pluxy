package responses

import (
	"bufio"
	"bytes"
	"context"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).
func ConvertCodexResponseToOpenAIResponses(ctx context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []string {
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		rawJSON = bytes.TrimSpace(rawJSON[5:])
		if typeResult := gjson.GetBytes(rawJSON, "type"); typeResult.Exists() {
			typeStr := typeResult.String()
			if typeStr == "response.created" || typeStr == "response.in_progress" || typeStr == "response.completed" {
				rawJSON, _ = sjson.SetBytes(rawJSON, "response.instructions", gjson.GetBytes(originalRequestRawJSON, "instructions").String())
			}
		}
		return []string{fmt.Sprintf("data: %s", string(rawJSON))}
	}
	return []string{string(rawJSON)}
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, modelName string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, _ *any) string {
	scanner := bufio.NewScanner(bytes.NewReader(rawJSON))
	buffer := make([]byte, 20_971_520)
	scanner.Buffer(buffer, 20_971_520)
	dataTag := []byte("data:")
	for scanner.Scan() {
		line := scanner.Bytes()

		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		line = bytes.TrimSpace(line[5:])

		rootResult := gjson.ParseBytes(line)
		// Verify this is a response.completed event

		if rootResult.Get("type").String() != "response.completed" {

			continue
		}
		responseResult := rootResult.Get("response")
		template := responseResult.Raw

		template, _ = sjson.Set(template, "instructions", gjson.GetBytes(originalRequestRawJSON, "instructions").String())

		return template
	}
	return ""
}
