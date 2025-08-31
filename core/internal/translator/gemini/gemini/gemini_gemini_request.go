// Package gemini provides in-provider request normalization for Gemini API.
// It ensures incoming v1beta requests meet minimal schema requirements
// expected by Google's Generative Language API.
package gemini

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertGeminiRequestToGemini normalizes Gemini v1beta requests.
//   - Adds a default role for each content if missing or invalid.
//     The first message defaults to "user", then alternates user/model when needed.
//
// It keeps the payload otherwise unchanged.
func ConvertGeminiRequestToGemini(_ string, rawJSON []byte, _ bool) []byte {
	// Fast path: if no contents field, return as-is
	contents := gjson.GetBytes(rawJSON, "contents")
	if !contents.Exists() {
		return rawJSON
	}

	// Walk contents and fix roles
	out := rawJSON
	prevRole := ""
	idx := 0
	contents.ForEach(func(_ gjson.Result, value gjson.Result) bool {
		role := value.Get("role").String()

		// Only user/model are valid for Gemini v1beta requests
		valid := role == "user" || role == "model"
		if role == "" || !valid {
			var newRole string
			if prevRole == "" {
				newRole = "user"
			} else if prevRole == "user" {
				newRole = "model"
			} else {
				newRole = "user"
			}
			path := fmt.Sprintf("contents.%d.role", idx)
			out, _ = sjson.SetBytes(out, path, newRole)
			role = newRole
		}

		prevRole = role
		idx++
		return true
	})

	return out
}
