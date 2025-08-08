// Package client defines the interface and base structure for AI API clients.
// It provides a common interface that all supported AI service clients must implement,
// including methods for sending messages, handling streams, and managing authentication.
package client

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/config"
)

// Client defines the interface that all AI API clients must implement.
// This interface provides methods for interacting with various AI services
// including sending messages, streaming responses, and managing authentication.
type Client interface {
	// GetRequestMutex returns the mutex used to synchronize requests for this client.
	// This ensures that only one request is processed at a time for quota management.
	GetRequestMutex() *sync.Mutex

	// GetUserAgent returns the User-Agent string used for HTTP requests.
	GetUserAgent() string

	// SendMessage sends a single message to the AI service and returns the response.
	// It takes the raw JSON request, model name, system instructions, conversation contents,
	// and tool declarations, then returns the response bytes and any error that occurred.
	SendMessage(ctx context.Context, rawJSON []byte, model string, systemInstruction *Content, contents []Content, tools []ToolDeclaration) ([]byte, *ErrorMessage)

	// SendMessageStream sends a message to the AI service and returns streaming responses.
	// It takes similar parameters to SendMessage but returns channels for streaming data
	// and errors, enabling real-time response processing.
	SendMessageStream(ctx context.Context, rawJSON []byte, model string, systemInstruction *Content, contents []Content, tools []ToolDeclaration, includeThoughts ...bool) (<-chan []byte, <-chan *ErrorMessage)

	// SendRawMessage sends a raw JSON message to the AI service without translation.
	// This method is used when the request is already in the service's native format.
	SendRawMessage(ctx context.Context, rawJSON []byte, alt string) ([]byte, *ErrorMessage)

	// SendRawMessageStream sends a raw JSON message and returns streaming responses.
	// Similar to SendRawMessage but for streaming responses.
	SendRawMessageStream(ctx context.Context, rawJSON []byte, alt string) (<-chan []byte, <-chan *ErrorMessage)

	// SendRawTokenCount sends a token count request to the AI service.
	// This method is used to estimate the number of tokens in a given text.
	SendRawTokenCount(ctx context.Context, rawJSON []byte, alt string) ([]byte, *ErrorMessage)

	// SaveTokenToFile saves the client's authentication token to a file.
	// This is used for persisting authentication state between sessions.
	SaveTokenToFile() error

	// IsModelQuotaExceeded checks if the specified model has exceeded its quota.
	// This helps with load balancing and automatic failover to alternative models.
	IsModelQuotaExceeded(model string) bool

	// GetEmail returns the email associated with the client's authentication.
	// This is used for logging and identification purposes.
	GetEmail() string
}

// ClientBase provides a common base structure for all AI API clients.
// It implements shared functionality such as request synchronization, HTTP client management,
// configuration access, token storage, and quota tracking.
type ClientBase struct {
	// RequestMutex ensures only one request is processed at a time for quota management.
	RequestMutex *sync.Mutex

	// httpClient is the HTTP client used for making API requests.
	httpClient *http.Client

	// cfg holds the application configuration.
	cfg *config.Config

	// tokenStorage manages authentication tokens for the client.
	tokenStorage auth.TokenStorage

	// modelQuotaExceeded tracks when models have exceeded their quota.
	// The map key is the model name, and the value is the time when the quota was exceeded.
	modelQuotaExceeded map[string]*time.Time
}

// GetRequestMutex returns the mutex used to synchronize requests for this client.
// This ensures that only one request is processed at a time for quota management.
func (c *ClientBase) GetRequestMutex() *sync.Mutex {
	return c.RequestMutex
}
