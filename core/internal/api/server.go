package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strings"
)

// Server represents the main API server.
// It encapsulates the Gin engine, HTTP server, handlers, and configuration.
type Server struct {
	engine   *gin.Engine
	server   *http.Server
	handlers *APIHandlers
	cfg      *config.Config
}

// NewServer creates and initializes a new API server instance.
// It sets up the Gin engine, middleware, routes, and handlers.
func NewServer(cfg *config.Config, cliClients []*client.Client) *Server {
	// Set gin mode
	if !cfg.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create handlers
	handlers := NewAPIHandlers(cliClients, cfg)

	// Create gin engine
	engine := gin.New()

	// Add middleware
	engine.Use(gin.Logger())
	engine.Use(gin.Recovery())
	engine.Use(corsMiddleware())

	// Create server instance
	s := &Server{
		engine:   engine,
		handlers: handlers,
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
	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(AuthMiddleware(s.cfg))
	{
		v1.GET("/models", s.handlers.Models)
		v1.POST("/chat/completions", s.handlers.ChatCompletions)
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
	s.engine.POST("/v1internal:method", s.handlers.CLIHandler)

}

// Start begins listening for and serving HTTP requests.
// It's a blocking call and will only return on an unrecoverable error.
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

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using API keys. If no API keys are configured, it allows all requests.
func AuthMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(cfg.ApiKeys) == 0 {
			c.Next()
			return
		}

		// Get the Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
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
		for i := range cfg.ApiKeys {
			if cfg.ApiKeys[i] == apiKey {
				foundKey = cfg.ApiKeys[i]
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
