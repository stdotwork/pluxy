// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This includes request logging middleware and response writer wrappers that capture
// request and response data for logging purposes while maintaining zero-latency performance.
package middleware

import (
	"bytes"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/logging"
)

// RequestInfo holds information about the current request for logging purposes.
type RequestInfo struct {
	URL     string
	Method  string
	Headers map[string][]string
	Body    []byte
}

// ResponseWriterWrapper wraps gin.ResponseWriter to capture response data for logging.
// It maintains zero-latency performance by prioritizing client response over logging operations.
type ResponseWriterWrapper struct {
	gin.ResponseWriter
	body         *bytes.Buffer
	isStreaming  bool
	streamWriter logging.StreamingLogWriter
	chunkChannel chan []byte
	logger       logging.RequestLogger
	requestInfo  *RequestInfo
	statusCode   int
	headers      map[string][]string
}

// NewResponseWriterWrapper creates a new response writer wrapper.
func NewResponseWriterWrapper(w gin.ResponseWriter, logger logging.RequestLogger, requestInfo *RequestInfo) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		logger:         logger,
		requestInfo:    requestInfo,
		headers:        make(map[string][]string),
	}
}

// Write intercepts response data while maintaining normal Gin functionality.
// CRITICAL: This method prioritizes client response (zero-latency) over logging operations.
func (w *ResponseWriterWrapper) Write(data []byte) (int, error) {
	// CRITICAL: Write to client first (zero latency)
	n, err := w.ResponseWriter.Write(data)

	// THEN: Handle logging based on response type
	if w.isStreaming {
		// For streaming responses: Send to async logging channel (non-blocking)
		if w.chunkChannel != nil {
			select {
			case w.chunkChannel <- append([]byte(nil), data...): // Non-blocking send with copy
			default: // Channel full, skip logging to avoid blocking
			}
		}
	} else {
		// For non-streaming responses: Buffer complete response
		w.body.Write(data)
	}

	return n, err
}

// WriteHeader captures the status code and detects streaming responses.
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode

	// Capture response headers
	for key, values := range w.ResponseWriter.Header() {
		w.headers[key] = values
	}

	// Detect streaming based on Content-Type
	contentType := w.ResponseWriter.Header().Get("Content-Type")
	w.isStreaming = w.detectStreaming(contentType)

	// If streaming, initialize streaming log writer
	if w.isStreaming && w.logger.IsEnabled() {
		streamWriter, err := w.logger.LogStreamingRequest(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			w.requestInfo.Body,
		)
		if err == nil {
			w.streamWriter = streamWriter
			w.chunkChannel = make(chan []byte, 100) // Buffered channel for async writes

			// Start async chunk processor
			go w.processStreamingChunks()

			// Write status immediately
			_ = streamWriter.WriteStatus(statusCode, w.headers)
		}
	}

	// Call original WriteHeader
	w.ResponseWriter.WriteHeader(statusCode)
}

// detectStreaming determines if the response is streaming based on Content-Type and request analysis.
func (w *ResponseWriterWrapper) detectStreaming(contentType string) bool {
	// Check Content-Type for Server-Sent Events
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}

	// Check request body for streaming indicators
	if w.requestInfo.Body != nil {
		bodyStr := string(w.requestInfo.Body)
		if strings.Contains(bodyStr, `"stream": true`) || strings.Contains(bodyStr, `"stream":true`) {
			return true
		}
	}

	return false
}

// processStreamingChunks handles async processing of streaming chunks.
func (w *ResponseWriterWrapper) processStreamingChunks() {
	if w.streamWriter == nil || w.chunkChannel == nil {
		return
	}

	for chunk := range w.chunkChannel {
		w.streamWriter.WriteChunkAsync(chunk)
	}
}

// Finalize completes the logging process for the response.
func (w *ResponseWriterWrapper) Finalize(c *gin.Context) error {
	if !w.logger.IsEnabled() {
		return nil
	}

	if w.isStreaming {
		// Close streaming channel and writer
		if w.chunkChannel != nil {
			close(w.chunkChannel)
			w.chunkChannel = nil
		}

		if w.streamWriter != nil {
			return w.streamWriter.Close()
		}
	} else {
		// Capture final status code and headers if not already captured
		finalStatusCode := w.statusCode
		if finalStatusCode == 0 {
			// Get status from underlying ResponseWriter if available
			if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok {
				finalStatusCode = statusWriter.Status()
			} else {
				finalStatusCode = 200 // Default
			}
		}

		// Capture final headers
		finalHeaders := make(map[string][]string)
		for key, values := range w.ResponseWriter.Header() {
			finalHeaders[key] = values
		}
		// Merge with any headers we captured earlier
		for key, values := range w.headers {
			finalHeaders[key] = values
		}

		var apiRequestBody []byte
		apiRequest, isExist := c.Get("API_REQUEST")
		if isExist {
			var ok bool
			apiRequestBody, ok = apiRequest.([]byte)
			if !ok {
				apiRequestBody = nil
			}
		}

		var apiResponseBody []byte
		apiResponse, isExist := c.Get("API_RESPONSE")
		if isExist {
			var ok bool
			apiResponseBody, ok = apiResponse.([]byte)
			if !ok {
				apiResponseBody = nil
			}
		}

		// Log complete non-streaming response
		return w.logger.LogRequest(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			w.requestInfo.Body,
			finalStatusCode,
			finalHeaders,
			w.body.Bytes(),
			apiRequestBody,
			apiResponseBody,
		)
	}

	return nil
}

// Status returns the HTTP status code of the response.
func (w *ResponseWriterWrapper) Status() int {
	if w.statusCode == 0 {
		return 200 // Default status code
	}
	return w.statusCode
}

// Size returns the size of the response body.
func (w *ResponseWriterWrapper) Size() int {
	if w.isStreaming {
		return -1 // Unknown size for streaming responses
	}
	return w.body.Len()
}

// Written returns whether the response has been written.
func (w *ResponseWriterWrapper) Written() bool {
	return w.statusCode != 0
}
