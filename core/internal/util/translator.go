// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for JSON manipulation, proxy configuration,
// and other common operations used across the application.
package util

import (
	"bytes"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Walk recursively traverses a JSON structure to find all occurrences of a specific field.
// It builds paths to each occurrence and adds them to the provided paths slice.
//
// Parameters:
//   - value: The gjson.Result object to traverse
//   - path: The current path in the JSON structure (empty string for root)
//   - field: The field name to search for
//   - paths: Pointer to a slice where found paths will be stored
//
// The function works recursively, building dot-notation paths to each occurrence
// of the specified field throughout the JSON structure.
func Walk(value gjson.Result, path, field string, paths *[]string) {
	switch value.Type {
	case gjson.JSON:
		// For JSON objects and arrays, iterate through each child
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			if path == "" {
				childPath = key.String()
			} else {
				childPath = path + "." + key.String()
			}
			if key.String() == field {
				*paths = append(*paths, childPath)
			}
			Walk(val, childPath, field, paths)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// Terminal types - no further traversal needed
	}
}

// RenameKey renames a key in a JSON string by moving its value to a new key path
// and then deleting the old key path.
//
// Parameters:
//   - jsonStr: The JSON string to modify
//   - oldKeyPath: The dot-notation path to the key that should be renamed
//   - newKeyPath: The dot-notation path where the value should be moved to
//
// Returns:
//   - string: The modified JSON string with the key renamed
//   - error: An error if the operation fails
//
// The function performs the rename in two steps:
// 1. Sets the value at the new key path
// 2. Deletes the old key path
func RenameKey(jsonStr, oldKeyPath, newKeyPath string) (string, error) {
	value := gjson.Get(jsonStr, oldKeyPath)

	if !value.Exists() {
		return "", fmt.Errorf("old key '%s' does not exist", oldKeyPath)
	}

	interimJson, err := sjson.SetRaw(jsonStr, newKeyPath, value.Raw)
	if err != nil {
		return "", fmt.Errorf("failed to set new key '%s': %w", newKeyPath, err)
	}

	finalJson, err := sjson.Delete(interimJson, oldKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to delete old key '%s': %w", oldKeyPath, err)
	}

	return finalJson, nil
}

// FixJSON converts non-standard JSON that uses single quotes for strings into
// RFC 8259-compliant JSON by converting those single-quoted strings to
// double-quoted strings with proper escaping.
//
// Examples:
//
//	{'a': 1, 'b': '2'}      => {"a": 1, "b": "2"}
//	{"t": 'He said "hi"'} => {"t": "He said \"hi\""}
//
// Rules:
//   - Existing double-quoted JSON strings are preserved as-is.
//   - Single-quoted strings are converted to double-quoted strings.
//   - Inside converted strings, any double quote is escaped (\").
//   - Common backslash escapes (\n, \r, \t, \b, \f, \\) are preserved.
//   - \' inside single-quoted strings becomes a literal ' in the output (no
//     escaping needed inside double quotes).
//   - Unicode escapes (\uXXXX) inside single-quoted strings are forwarded.
//   - The function does not attempt to fix other non-JSON features beyond quotes.
func FixJSON(input string) string {
	var out bytes.Buffer

	inDouble := false
	inSingle := false
	escaped := false // applies within the current string state

	// Helper to write a rune, escaping double quotes when inside a converted
	// single-quoted string (which becomes a double-quoted string in output).
	writeConverted := func(r rune) {
		if r == '"' {
			out.WriteByte('\\')
			out.WriteByte('"')
			return
		}
		out.WriteRune(r)
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if inDouble {
			out.WriteRune(r)
			if escaped {
				// end of escape sequence in a standard JSON string
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inDouble = false
			}
			continue
		}

		if inSingle {
			if escaped {
				// Handle common escape sequences after a backslash within a
				// single-quoted string
				escaped = false
				switch r {
				case 'n', 'r', 't', 'b', 'f', '/', '"':
					// Keep the backslash and the character (except for '"' which
					// rarely appears, but if it does, keep as \" to remain valid)
					out.WriteByte('\\')
					out.WriteRune(r)
				case '\\':
					out.WriteByte('\\')
					out.WriteByte('\\')
				case '\'':
					// \' inside single-quoted becomes a literal '
					out.WriteRune('\'')
				case 'u':
					// Forward \uXXXX if possible
					out.WriteByte('\\')
					out.WriteByte('u')
					// Copy up to next 4 hex digits if present
					for k := 0; k < 4 && i+1 < len(runes); k++ {
						peek := runes[i+1]
						// simple hex check
						if (peek >= '0' && peek <= '9') || (peek >= 'a' && peek <= 'f') || (peek >= 'A' && peek <= 'F') {
							out.WriteRune(peek)
							i++
						} else {
							break
						}
					}
				default:
					// Unknown escape: preserve the backslash and the char
					out.WriteByte('\\')
					out.WriteRune(r)
				}
				continue
			}

			if r == '\\' { // start escape sequence
				escaped = true
				continue
			}
			if r == '\'' { // end of single-quoted string
				out.WriteByte('"')
				inSingle = false
				continue
			}
			// regular char inside converted string; escape double quotes
			writeConverted(r)
			continue
		}

		// Outside any string
		if r == '"' {
			inDouble = true
			out.WriteRune(r)
			continue
		}
		if r == '\'' { // start of non-standard single-quoted string
			inSingle = true
			out.WriteByte('"')
			continue
		}
		out.WriteRune(r)
	}

	// If input ended while still inside a single-quoted string, close it to
	// produce the best-effort valid JSON.
	if inSingle {
		out.WriteByte('"')
	}

	return out.String()
}

// SanitizeSchemaForGemini removes JSON Schema fields that are incompatible with Gemini API
// to prevent "Proto field is not repeating, cannot start list" errors.
//
// Parameters:
//   - schemaJSON: The JSON schema string to sanitize
//
// Returns:
//   - string: The sanitized schema string
//   - error: An error if the operation fails
//
// This function removes the following incompatible fields:
// - additionalProperties: Not supported in Gemini function declarations
// - $schema: JSON Schema meta-schema identifier, not needed for API
// - allOf/anyOf/oneOf: Union type constructs not supported
// - exclusiveMinimum/exclusiveMaximum: Advanced validation constraints
// - patternProperties: Advanced property pattern matching
// - dependencies: Property dependencies not supported
// - type arrays: Converts ["string", "null"] to just "string"
func SanitizeSchemaForGemini(schemaJSON string) (string, error) {
	// Remove top-level incompatible fields
	fieldsToRemove := []string{
		"additionalProperties",
		"$schema",
		"allOf",
		"anyOf",
		"oneOf",
		"exclusiveMinimum",
		"exclusiveMaximum",
		"patternProperties",
		"dependencies",
	}

	result := schemaJSON
	var err error

	for _, field := range fieldsToRemove {
		result, err = sjson.Delete(result, field)
		if err != nil {
			continue // Continue even if deletion fails
		}
	}

	// Handle type arrays by converting them to single types
	result = sanitizeTypeFields(result)

	// Recursively clean nested objects
	result = cleanNestedSchemas(result)

	return result, nil
}

// sanitizeTypeFields converts type arrays to single types for Gemini compatibility
func sanitizeTypeFields(jsonStr string) string {
	// Parse the JSON to find all "type" fields
	parsed := gjson.Parse(jsonStr)
	result := jsonStr

	// Walk through all paths to find type fields
	var typeFields []string
	walkForTypeFields(parsed, "", &typeFields)

	// Process each type field
	for _, path := range typeFields {
		typeValue := gjson.Get(result, path)
		if typeValue.IsArray() {
			// Convert array to single type (prioritize string, then others)
			arr := typeValue.Array()
			if len(arr) > 0 {
				var preferredType string
				for _, t := range arr {
					typeStr := t.String()
					if typeStr == "string" {
						preferredType = "string"
						break
					} else if typeStr == "number" || typeStr == "integer" {
						preferredType = typeStr
						if preferredType == "" {
							preferredType = typeStr
						}
					} else if preferredType == "" {
						preferredType = typeStr
					}
				}
				if preferredType != "" {
					result, _ = sjson.Set(result, path, preferredType)
				}
			}
		}
	}

	return result
}

// walkForTypeFields recursively finds all "type" field paths in the JSON
func walkForTypeFields(value gjson.Result, path string, paths *[]string) {
	switch value.Type {
	case gjson.JSON:
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			if path == "" {
				childPath = key.String()
			} else {
				childPath = path + "." + key.String()
			}
			if key.String() == "type" {
				*paths = append(*paths, childPath)
			}
			walkForTypeFields(val, childPath, paths)
			return true
		})
	}
}

// cleanNestedSchemas recursively removes incompatible fields from nested schema objects
func cleanNestedSchemas(jsonStr string) string {
	fieldsToRemove := []string{"allOf", "anyOf", "oneOf", "exclusiveMinimum", "exclusiveMaximum"}

	// Find all nested paths that might contain these fields
	var pathsToClean []string
	parsed := gjson.Parse(jsonStr)
	findNestedSchemaPaths(parsed, "", fieldsToRemove, &pathsToClean)

	result := jsonStr
	// Remove fields from all found paths
	for _, path := range pathsToClean {
		result, _ = sjson.Delete(result, path)
	}

	return result
}

// findNestedSchemaPaths recursively finds paths containing incompatible schema fields
func findNestedSchemaPaths(value gjson.Result, path string, fieldsToFind []string, paths *[]string) {
	switch value.Type {
	case gjson.JSON:
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			if path == "" {
				childPath = key.String()
			} else {
				childPath = path + "." + key.String()
			}

			// Check if this key is one we want to remove
			for _, field := range fieldsToFind {
				if key.String() == field {
					*paths = append(*paths, childPath)
					break
				}
			}

			findNestedSchemaPaths(val, childPath, fieldsToFind, paths)
			return true
		})
	}
}
