package api

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/luispater/CLIProxyAPI/internal/client"
	log "github.com/sirupsen/logrus"
	"net/http"
	"strings"
)

// Server represents the API server
type Server struct {
	engine   *gin.Engine
	server   *http.Server
	handlers *APIHandlers
	cfg      *ServerConfig
}

// ServerConfig contains configuration for the API server
type ServerConfig struct {
	Port    string
	Debug   bool
	ApiKeys []string
}

// NewServer creates a new API server instance
func NewServer(config *ServerConfig, cliClients []*client.Client) *Server {
	// Set gin mode
	if !config.Debug {
		gin.SetMode(gin.ReleaseMode)
	}

	// Create handlers
	handlers := NewAPIHandlers(cliClients, config.Debug)

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
		cfg:      config,
	}

	// Setup routes
	s.setupRoutes()

	// Create HTTP server
	s.server = &http.Server{
		Addr:    ":" + config.Port,
		Handler: engine,
	}

	return s
}

// setupRoutes configures the API routes
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
}

// Start starts the API server
func (s *Server) Start() error {
	log.Debugf("Starting API server on %s", s.server.Addr)

	// Start the HTTP server
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("failed to start HTTP server: %v", err)
	}

	return nil
}

// Stop gracefully stops the API server
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	// Shutdown the HTTP server
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", err)
	}

	log.Debug("API server stopped")
	return nil
}

// corsMiddleware adds CORS headers
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

// AuthMiddleware authenticates requests using API keys
func AuthMiddleware(cfg *ServerConfig) gin.HandlerFunc {
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
