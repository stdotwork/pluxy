// Package logging provides request logging functionality for the CLI Proxy API server.
// It handles capturing and storing detailed HTTP request and response data when enabled
// through configuration, supporting both regular and streaming responses.
package logging

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// RequestLogger defines the interface for logging HTTP requests and responses.
type RequestLogger interface {
	// LogRequest logs a complete non-streaming request/response cycle
	LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte) error

	// LogStreamingRequest initiates logging for a streaming request and returns a writer for chunks
	LogStreamingRequest(url, method string, headers map[string][]string, body []byte) (StreamingLogWriter, error)

	// IsEnabled returns whether request logging is currently enabled
	IsEnabled() bool
}

// StreamingLogWriter handles real-time logging of streaming response chunks.
type StreamingLogWriter interface {
	// WriteChunkAsync writes a response chunk asynchronously (non-blocking)
	WriteChunkAsync(chunk []byte)

	// WriteStatus writes the response status and headers to the log
	WriteStatus(status int, headers map[string][]string) error

	// Close finalizes the log file and cleans up resources
	Close() error
}

// FileRequestLogger implements RequestLogger using file-based storage.
type FileRequestLogger struct {
	enabled bool
	logsDir string
}

// NewFileRequestLogger creates a new file-based request logger.
func NewFileRequestLogger(enabled bool, logsDir string) *FileRequestLogger {
	return &FileRequestLogger{
		enabled: enabled,
		logsDir: logsDir,
	}
}

// IsEnabled returns whether request logging is currently enabled.
func (l *FileRequestLogger) IsEnabled() bool {
	return l.enabled
}

// LogRequest logs a complete non-streaming request/response cycle to a file.
func (l *FileRequestLogger) LogRequest(url, method string, requestHeaders map[string][]string, body []byte, statusCode int, responseHeaders map[string][]string, response, apiRequest, apiResponse []byte) error {
	if !l.enabled {
		return nil
	}

	// Ensure logs directory exists
	if err := l.ensureLogsDir(); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Generate filename
	filename := l.generateFilename(url)
	filePath := filepath.Join(l.logsDir, filename)

	// Decompress response if needed
	decompressedResponse, err := l.decompressResponse(responseHeaders, response)
	if err != nil {
		// If decompression fails, log the error but continue with original response
		decompressedResponse = append(response, []byte(fmt.Sprintf("\n[DECOMPRESSION ERROR: %v]", err))...)
	}

	// Create log content
	content := l.formatLogContent(url, method, requestHeaders, body, apiRequest, apiResponse, decompressedResponse, statusCode, responseHeaders)

	// Write to file
	if err = os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write log file: %w", err)
	}

	return nil
}

// LogStreamingRequest initiates logging for a streaming request.
func (l *FileRequestLogger) LogStreamingRequest(url, method string, headers map[string][]string, body []byte) (StreamingLogWriter, error) {
	if !l.enabled {
		return &NoOpStreamingLogWriter{}, nil
	}

	// Ensure logs directory exists
	if err := l.ensureLogsDir(); err != nil {
		return nil, fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Generate filename
	filename := l.generateFilename(url)
	filePath := filepath.Join(l.logsDir, filename)

	// Create and open file
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}

	// Write initial request information
	requestInfo := l.formatRequestInfo(url, method, headers, body)
	if _, err = file.WriteString(requestInfo); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("failed to write request info: %w", err)
	}

	// Create streaming writer
	writer := &FileStreamingLogWriter{
		file:      file,
		chunkChan: make(chan []byte, 100), // Buffered channel for async writes
		closeChan: make(chan struct{}),
		errorChan: make(chan error, 1),
	}

	// Start async writer goroutine
	go writer.asyncWriter()

	return writer, nil
}

// ensureLogsDir creates the logs directory if it doesn't exist.
func (l *FileRequestLogger) ensureLogsDir() error {
	if _, err := os.Stat(l.logsDir); os.IsNotExist(err) {
		return os.MkdirAll(l.logsDir, 0755)
	}
	return nil
}

// generateFilename creates a sanitized filename from the URL path and current timestamp.
func (l *FileRequestLogger) generateFilename(url string) string {
	// Extract path from URL
	path := url
	if strings.Contains(url, "?") {
		path = strings.Split(url, "?")[0]
	}

	// Remove leading slash
	if strings.HasPrefix(path, "/") {
		path = path[1:]
	}

	// Sanitize path for filename
	sanitized := l.sanitizeForFilename(path)

	// Add timestamp
	timestamp := time.Now().UnixNano()

	return fmt.Sprintf("%s-%d.log", sanitized, timestamp)
}

// sanitizeForFilename replaces characters that are not safe for filenames.
func (l *FileRequestLogger) sanitizeForFilename(path string) string {
	// Replace slashes with hyphens
	sanitized := strings.ReplaceAll(path, "/", "-")

	// Replace colons with hyphens
	sanitized = strings.ReplaceAll(sanitized, ":", "-")

	// Replace other problematic characters with hyphens
	reg := regexp.MustCompile(`[<>:"|?*\s]`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	// Remove multiple consecutive hyphens
	reg = regexp.MustCompile(`-+`)
	sanitized = reg.ReplaceAllString(sanitized, "-")

	// Remove leading/trailing hyphens
	sanitized = strings.Trim(sanitized, "-")

	// Handle empty result
	if sanitized == "" {
		sanitized = "root"
	}

	return sanitized
}

// formatLogContent creates the complete log content for non-streaming requests.
func (l *FileRequestLogger) formatLogContent(url, method string, headers map[string][]string, body, apiRequest, apiResponse, response []byte, status int, responseHeaders map[string][]string) string {
	var content strings.Builder

	// Request info
	content.WriteString(l.formatRequestInfo(url, method, headers, body))

	content.WriteString("=== API REQUEST ===\n")
	content.Write(apiRequest)
	content.WriteString("\n\n")

	content.WriteString("=== API RESPONSE ===\n")
	content.Write(apiResponse)
	content.WriteString("\n\n")

	// Response section
	content.WriteString("=== RESPONSE ===\n")
	content.WriteString(fmt.Sprintf("Status: %d\n", status))

	if responseHeaders != nil {
		for key, values := range responseHeaders {
			for _, value := range values {
				content.WriteString(fmt.Sprintf("%s: %s\n", key, value))
			}
		}
	}

	content.WriteString("\n")
	content.Write(response)
	content.WriteString("\n")

	return content.String()
}

// decompressResponse decompresses response data based on Content-Encoding header.
func (l *FileRequestLogger) decompressResponse(responseHeaders map[string][]string, response []byte) ([]byte, error) {
	if responseHeaders == nil || len(response) == 0 {
		return response, nil
	}

	// Check Content-Encoding header
	var contentEncoding string
	for key, values := range responseHeaders {
		if strings.ToLower(key) == "content-encoding" && len(values) > 0 {
			contentEncoding = strings.ToLower(values[0])
			break
		}
	}

	switch contentEncoding {
	case "gzip":
		return l.decompressGzip(response)
	case "deflate":
		return l.decompressDeflate(response)
	default:
		// No compression or unsupported compression
		return response, nil
	}
}

// decompressGzip decompresses gzip-encoded data.
func (l *FileRequestLogger) decompressGzip(data []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress gzip data: %w", err)
	}

	return decompressed, nil
}

// decompressDeflate decompresses deflate-encoded data.
func (l *FileRequestLogger) decompressDeflate(data []byte) ([]byte, error) {
	reader := flate.NewReader(bytes.NewReader(data))
	defer func() {
		_ = reader.Close()
	}()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress deflate data: %w", err)
	}

	return decompressed, nil
}

// formatRequestInfo creates the request information section of the log.
func (l *FileRequestLogger) formatRequestInfo(url, method string, headers map[string][]string, body []byte) string {
	var content strings.Builder

	content.WriteString("=== REQUEST INFO ===\n")
	content.WriteString(fmt.Sprintf("URL: %s\n", url))
	content.WriteString(fmt.Sprintf("Method: %s\n", method))
	content.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	content.WriteString("\n")

	content.WriteString("=== HEADERS ===\n")
	for key, values := range headers {
		for _, value := range values {
			content.WriteString(fmt.Sprintf("%s: %s\n", key, value))
		}
	}
	content.WriteString("\n")

	content.WriteString("=== REQUEST BODY ===\n")
	content.Write(body)
	content.WriteString("\n\n")

	return content.String()
}

// FileStreamingLogWriter implements StreamingLogWriter for file-based streaming logs.
type FileStreamingLogWriter struct {
	file          *os.File
	chunkChan     chan []byte
	closeChan     chan struct{}
	errorChan     chan error
	statusWritten bool
}

// WriteChunkAsync writes a response chunk asynchronously (non-blocking).
func (w *FileStreamingLogWriter) WriteChunkAsync(chunk []byte) {
	if w.chunkChan == nil {
		return
	}

	// Make a copy of the chunk to avoid data races
	chunkCopy := make([]byte, len(chunk))
	copy(chunkCopy, chunk)

	// Non-blocking send
	select {
	case w.chunkChan <- chunkCopy:
	default:
		// Channel is full, skip this chunk to avoid blocking
	}
}

// WriteStatus writes the response status and headers to the log.
func (w *FileStreamingLogWriter) WriteStatus(status int, headers map[string][]string) error {
	if w.file == nil || w.statusWritten {
		return nil
	}

	var content strings.Builder
	content.WriteString("========================================\n")
	content.WriteString("=== RESPONSE ===\n")
	content.WriteString(fmt.Sprintf("Status: %d\n", status))

	for key, values := range headers {
		for _, value := range values {
			content.WriteString(fmt.Sprintf("%s: %s\n", key, value))
		}
	}
	content.WriteString("\n")

	_, err := w.file.WriteString(content.String())
	if err == nil {
		w.statusWritten = true
	}
	return err
}

// Close finalizes the log file and cleans up resources.
func (w *FileStreamingLogWriter) Close() error {
	if w.chunkChan != nil {
		close(w.chunkChan)
	}

	// Wait for async writer to finish
	if w.closeChan != nil {
		<-w.closeChan
		w.chunkChan = nil
	}

	if w.file != nil {
		return w.file.Close()
	}

	return nil
}

// asyncWriter runs in a goroutine to handle async chunk writing.
func (w *FileStreamingLogWriter) asyncWriter() {
	defer close(w.closeChan)

	for chunk := range w.chunkChan {
		if w.file != nil {
			_, _ = w.file.Write(chunk)
		}
	}
}

// NoOpStreamingLogWriter is a no-operation implementation for when logging is disabled.
type NoOpStreamingLogWriter struct{}

func (w *NoOpStreamingLogWriter) WriteChunkAsync(chunk []byte) {}
func (w *NoOpStreamingLogWriter) WriteStatus(status int, headers map[string][]string) error {
	return nil
}
func (w *NoOpStreamingLogWriter) Close() error { return nil }
