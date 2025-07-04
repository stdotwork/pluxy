package api

// ErrorResponse represents a standard error response format for the API.
// It contains a single ErrorDetail field.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail provides specific information about an error that occurred.
// It includes a human-readable message, an error type, and an optional error code.
type ErrorDetail struct {
	// A human-readable message providing more details about the error.
	Message string `json:"message"`
	// The type of error that occurred (e.g., "invalid_request_error").
	Type string `json:"type"`
	// A short code identifying the error, if applicable.
	Code string `json:"code,omitempty"`
}
