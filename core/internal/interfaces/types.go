// Package interfaces defines the core interfaces and shared structures for the CLI Proxy API server.
// These interfaces provide a common contract for different components of the application,
// such as AI service clients, API handlers, and data models.
package interfaces

import "context"

// TranslateRequestFunc defines a function type for translating API requests between different formats.
// It takes a model name, raw JSON request data, and a streaming flag, returning the translated request.
//
// Parameters:
//   - string: The model name
//   - []byte: The raw JSON request data
//   - bool: A flag indicating whether the request is for streaming
//
// Returns:
//   - []byte: The translated request data
type TranslateRequestFunc func(string, []byte, bool) []byte

// TranslateResponseFunc defines a function type for translating streaming API responses.
// It processes response data and returns an array of translated response strings.
//
// Parameters:
//   - ctx: The context for the request
//   - modelName: The model name
//   - rawJSON: The raw JSON response data
//   - param: Additional parameters for translation
//
// Returns:
//   - []string: An array of translated response strings
type TranslateResponseFunc func(ctx context.Context, modelName string, rawJSON []byte, param *any) []string

// TranslateResponseNonStreamFunc defines a function type for translating non-streaming API responses.
// It processes response data and returns a single translated response string.
//
// Parameters:
//   - ctx: The context for the request
//   - modelName: The model name
//   - rawJSON: The raw JSON response data
//   - param: Additional parameters for translation
//
// Returns:
//   - string: A single translated response string
type TranslateResponseNonStreamFunc func(ctx context.Context, modelName string, rawJSON []byte, param *any) string

// TranslateResponse contains both streaming and non-streaming response translation functions.
// This structure allows clients to handle both types of API responses appropriately.
type TranslateResponse struct {
	// Stream handles streaming response translation.
	Stream TranslateResponseFunc

	// NonStream handles non-streaming response translation.
	NonStream TranslateResponseNonStreamFunc
}
