// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/logging"
)

// RequestLoggingMiddleware creates a Gin middleware function that logs HTTP requests and responses
// when enabled through the provided logger. The middleware has zero overhead when logging is disabled.
func RequestLoggingMiddleware(logger logging.RequestLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Early return if logging is disabled (zero overhead)
		if !logger.IsEnabled() {
			c.Next()
			return
		}

		// Capture request information
		requestInfo, err := captureRequestInfo(c)
		if err != nil {
			// Log error but continue processing
			// In a real implementation, you might want to use a proper logger here
			c.Next()
			return
		}

		// Create response writer wrapper
		wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
		c.Writer = wrapper

		// Process the request
		c.Next()

		// Finalize logging after request processing
		if err = wrapper.Finalize(c); err != nil {
			// Log error but don't interrupt the response
			// In a real implementation, you might want to use a proper logger here
		}
	}
}

// captureRequestInfo extracts and captures request information for logging.
func captureRequestInfo(c *gin.Context) (*RequestInfo, error) {
	// Capture URL
	url := c.Request.URL.String()
	if c.Request.URL.Path != "" {
		url = c.Request.URL.Path
		if c.Request.URL.RawQuery != "" {
			url += "?" + c.Request.URL.RawQuery
		}
	}

	// Capture method
	method := c.Request.Method

	// Capture headers
	headers := make(map[string][]string)
	for key, values := range c.Request.Header {
		headers[key] = values
	}

	// Capture request body
	var body []byte
	if c.Request.Body != nil {
		// Read the body
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}

		// Restore the body for the actual request processing
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		body = bodyBytes
	}

	return &RequestInfo{
		URL:     url,
		Method:  method,
		Headers: headers,
		Body:    body,
	}, nil
}
