// Package cli provides request translation functionality for Gemini CLI API.
// It handles the conversion and formatting of CLI tool responses, specifically
// transforming between different JSON formats to ensure proper conversation flow
// and API compatibility. The package focuses on intelligently grouping function
// calls with their corresponding responses, converting from linear format to
// grouped format where function calls and responses are properly associated.
package cli

import (
	"encoding/json"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// FunctionCallGroup represents a group of function calls and their responses
type FunctionCallGroup struct {
	ModelContent    map[string]interface{}
	FunctionCalls   []gjson.Result
	ResponsesNeeded int
}

// FixCLIToolResponse performs sophisticated tool response format conversion and grouping.
// This function transforms the CLI tool response format by intelligently grouping function calls
// with their corresponding responses, ensuring proper conversation flow and API compatibility.
// It converts from a linear format (1.json) to a grouped format (2.json) where function calls
// and their responses are properly associated and structured.
func FixCLIToolResponse(input string) (string, error) {
	// Parse the input JSON to extract the conversation structure
	parsed := gjson.Parse(input)

	// Extract the contents array which contains the conversation messages
	contents := parsed.Get("request.contents")
	if !contents.Exists() {
		// log.Debugf(input)
		return input, fmt.Errorf("contents not found in input")
	}

	// Initialize data structures for processing and grouping
	var newContents []interface{}          // Final processed contents array
	var pendingGroups []*FunctionCallGroup // Groups awaiting completion with responses
	var collectedResponses []gjson.Result  // Standalone responses to be matched

	// Process each content object in the conversation
	// This iterates through messages and groups function calls with their responses
	contents.ForEach(func(key, value gjson.Result) bool {
		role := value.Get("role").String()
		parts := value.Get("parts")

		// Check if this content has function responses
		var responsePartsInThisContent []gjson.Result
		parts.ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				responsePartsInThisContent = append(responsePartsInThisContent, part)
			}
			return true
		})

		// If this content has function responses, collect them
		if len(responsePartsInThisContent) > 0 {
			collectedResponses = append(collectedResponses, responsePartsInThisContent...)

			// Check if any pending groups can be satisfied
			for i := len(pendingGroups) - 1; i >= 0; i-- {
				group := pendingGroups[i]
				if len(collectedResponses) >= group.ResponsesNeeded {
					// Take the needed responses for this group
					groupResponses := collectedResponses[:group.ResponsesNeeded]
					collectedResponses = collectedResponses[group.ResponsesNeeded:]

					// Create merged function response content
					var responseParts []interface{}
					for _, response := range groupResponses {
						var responseMap map[string]interface{}
						errUnmarshal := json.Unmarshal([]byte(response.Raw), &responseMap)
						if errUnmarshal != nil {
							log.Warnf("failed to unmarshal function response: %v\n", errUnmarshal)
							continue
						}
						responseParts = append(responseParts, responseMap)
					}

					if len(responseParts) > 0 {
						functionResponseContent := map[string]interface{}{
							"parts": responseParts,
							"role":  "function",
						}
						newContents = append(newContents, functionResponseContent)
					}

					// Remove this group as it's been satisfied
					pendingGroups = append(pendingGroups[:i], pendingGroups[i+1:]...)
					break
				}
			}

			return true // Skip adding this content, responses are merged
		}

		// If this is a model with function calls, create a new group
		if role == "model" {
			var functionCallsInThisModel []gjson.Result
			parts.ForEach(func(_, part gjson.Result) bool {
				if part.Get("functionCall").Exists() {
					functionCallsInThisModel = append(functionCallsInThisModel, part)
				}
				return true
			})

			if len(functionCallsInThisModel) > 0 {
				// Add the model content
				var contentMap map[string]interface{}
				errUnmarshal := json.Unmarshal([]byte(value.Raw), &contentMap)
				if errUnmarshal != nil {
					log.Warnf("failed to unmarshal model content: %v\n", errUnmarshal)
					return true
				}
				newContents = append(newContents, contentMap)

				// Create a new group for tracking responses
				group := &FunctionCallGroup{
					ModelContent:    contentMap,
					FunctionCalls:   functionCallsInThisModel,
					ResponsesNeeded: len(functionCallsInThisModel),
				}
				pendingGroups = append(pendingGroups, group)
			} else {
				// Regular model content without function calls
				var contentMap map[string]interface{}
				errUnmarshal := json.Unmarshal([]byte(value.Raw), &contentMap)
				if errUnmarshal != nil {
					log.Warnf("failed to unmarshal content: %v\n", errUnmarshal)
					return true
				}
				newContents = append(newContents, contentMap)
			}
		} else {
			// Non-model content (user, etc.)
			var contentMap map[string]interface{}
			errUnmarshal := json.Unmarshal([]byte(value.Raw), &contentMap)
			if errUnmarshal != nil {
				log.Warnf("failed to unmarshal content: %v\n", errUnmarshal)
				return true
			}
			newContents = append(newContents, contentMap)
		}

		return true
	})

	// Handle any remaining pending groups with remaining responses
	for _, group := range pendingGroups {
		if len(collectedResponses) >= group.ResponsesNeeded {
			groupResponses := collectedResponses[:group.ResponsesNeeded]
			collectedResponses = collectedResponses[group.ResponsesNeeded:]

			var responseParts []interface{}
			for _, response := range groupResponses {
				var responseMap map[string]interface{}
				errUnmarshal := json.Unmarshal([]byte(response.Raw), &responseMap)
				if errUnmarshal != nil {
					log.Warnf("failed to unmarshal function response: %v\n", errUnmarshal)
					continue
				}
				responseParts = append(responseParts, responseMap)
			}

			if len(responseParts) > 0 {
				functionResponseContent := map[string]interface{}{
					"parts": responseParts,
					"role":  "function",
				}
				newContents = append(newContents, functionResponseContent)
			}
		}
	}

	// Update the original JSON with the new contents
	result := input
	newContentsJSON, _ := json.Marshal(newContents)
	result, _ = sjson.Set(result, "request.contents", json.RawMessage(newContentsJSON))

	return result, nil
}
