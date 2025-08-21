// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude, Gemini).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers/claude"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers/gemini"
	"github.com/luispater/CLIProxyAPI/internal/api/handlers/openai"
	"github.com/luispater/CLIProxyAPI/internal/api/middleware"
	"github.com/luispater/CLIProxyAPI/internal/config"
	"github.com/luispater/CLIProxyAPI/internal/interfaces"
	"github.com/luispater/CLIProxyAPI/internal/logging"
	log "github.com/sirupsen/logrus"
)

// Server represents the main API server.
// It encapsulates the Gin engine, HTTP server, handlers, and configuration.
type Server struct {
	// engine is the Gin web framework engine instance.
	engine *gin.Engine

	// server is the underlying HTTP server.
	server *http.Server

	// handlers contains the API handlers for processing requests.
	handlers *handlers.BaseAPIHandler

	// cfg holds the current server configuration.
	cfg *config.Config
}

// NewServer creates and initializes a new API server instance.
// It sets up the Gin engine, middleware, routes, and handlers.
//
// Parameters:
//   - cfg: The server configuration
//   - cliClients: A slice of AI service clients
//
// Returns:
//   - *Server: A new server instance
func NewServer(cfg *config.Config, cliClients []interfaces.Client) *Server {
	// Set gin mode
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create gin engine
	engine := gin.New()

	// Add middleware
	engine.Use(gin.Logger())
	engine.Use(gin.Recovery())

	// Add request logging middleware (positioned after recovery, before auth)
	requestLogger := logging.NewFileRequestLogger(cfg.RequestLog, "logs")
	engine.Use(middleware.RequestLoggingMiddleware(requestLogger))

	engine.Use(corsMiddleware())

	// Create server instance
	s := &Server{
		engine:   engine,
		handlers: handlers.NewBaseAPIHandlers(cliClients, cfg),
		cfg:      cfg,
	}

	// Setup routes
	s.setupRoutes()

	// Create HTTP server
	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: engine,
	}

	return s
}

// setupRoutes configures the API routes for the server.
// It defines the endpoints and associates them with their respective handlers.
func (s *Server) setupRoutes() {
	openaiHandlers := openai.NewOpenAIAPIHandler(s.handlers)
	geminiHandlers := gemini.NewGeminiAPIHandler(s.handlers)
	geminiCLIHandlers := gemini.NewGeminiCLIAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)

	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.cfg))
	{
		v1.GET("/models", openaiHandlers.OpenAIModels)
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
	}

	// Gemini compatible API routes
	v1beta := s.engine.Group("/v1beta")
	v1beta.Use(AuthMiddleware(s.cfg))
	{
		v1beta.GET("/models", geminiHandlers.GeminiModels)
		v1beta.POST("/models/:action", geminiHandlers.GeminiHandler)
		v1beta.GET("/models/:action", geminiHandlers.GeminiGetHandler)
	}

	// Root endpoint
	s.engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "CLI Proxy API Server",
			"version": "1.0.0",
			"endpoints": []string{
				"POST /v1/chat/completions",
				"GET /v1/models",
			},
		})
	})
	s.engine.POST("/v1internal:method", geminiCLIHandlers.CLIHandler)
}

// Start begins listening for and serving HTTP requests.
// It's a blocking call and will only return on an unrecoverable error.
//
// Returns:
//   - error: An error if the server fails to start
func (s *Server) Start() error {
	log.Debugf("Starting API server on %s", s.server.Addr)

	// Start the HTTP server.
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to start HTTP server: %v", err)
	}

	return nil
}

// Stop gracefully shuts down the API server without interrupting any
// active connections.
//
// Parameters:
//   - ctx: The context for graceful shutdown
//
// Returns:
//   - error: An error if the server fails to stop
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	// Shutdown the HTTP server.
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", err)
	}

	log.Debug("API server stopped")
	return nil
}

// corsMiddleware returns a Gin middleware handler that adds CORS headers
// to every response, allowing cross-origin requests.
//
// Returns:
//   - gin.HandlerFunc: The CORS middleware handler
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// UpdateClients updates the server's client list and configuration.
// This method is called when the configuration or authentication tokens change.
//
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (s *Server) UpdateClients(clients []interfaces.Client, cfg *config.Config) {
	s.cfg = cfg
	s.handlers.UpdateClients(clients, cfg)
	log.Infof("server clients and configuration updated: %d clients", len(clients))
}

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using API keys. If no API keys are configured, it allows all requests.
//
// Parameters:
//   - cfg: The server configuration containing API keys
//
// Returns:
//   - gin.HandlerFunc: The authentication middleware handler
func AuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(cfg.APIKeys) == 0 {
			c.Next()
			return
		}

		// Get the Authorization header
		authHeader := c.GetHeader("Authorization")
		authHeaderGoogle := c.GetHeader("X-Goog-Api-Key")
		authHeaderAnthropic := c.GetHeader("X-Api-Key")

		// Get the API key from the query parameter
		apiKeyQuery, _ := c.GetQuery("key")

		if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && apiKeyQuery == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Missing API key",
			})
			return
		}

		// Extract the API key
		parts := strings.Split(authHeader, " ")
		var apiKey string
		if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
			apiKey = parts[1]
		} else {
			apiKey = authHeader
		}

		// Find the API key in the in-memory list
		var foundKey string
		for i := range cfg.APIKeys {
			if cfg.APIKeys[i] == apiKey || cfg.APIKeys[i] == authHeaderGoogle || cfg.APIKeys[i] == authHeaderAnthropic || cfg.APIKeys[i] == apiKeyQuery {
				foundKey = cfg.APIKeys[i]
				break
			}
		}
		if foundKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid API key",
			})
			return
		}

		// Store the API key and user in the context
		c.Set("apiKey", foundKey)

		c.Next()
	}
}
