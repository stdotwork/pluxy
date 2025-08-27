// Package registry provides centralized model management for all AI service providers.
// It implements a dynamic model registry with reference counting to track active clients
// and automatically hide models when no clients are available or when quota is exceeded.
package registry

import (
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// ModelInfo represents information about an available model
type ModelInfo struct {
	// ID is the unique identifier for the model
	ID string `json:"id"`
	// Object type for the model (typically "model")
	Object string `json:"object"`
	// Created timestamp when the model was created
	Created int64 `json:"created"`
	// OwnedBy indicates the organization that owns the model
	OwnedBy string `json:"owned_by"`
	// Type indicates the model type (e.g., "claude", "gemini", "openai")
	Type string `json:"type"`
	// DisplayName is the human-readable name for the model
	DisplayName string `json:"display_name,omitempty"`
	// Name is used for Gemini-style model names
	Name string `json:"name,omitempty"`
	// Version is the model version
	Version string `json:"version,omitempty"`
	// Description provides detailed information about the model
	Description string `json:"description,omitempty"`
	// InputTokenLimit is the maximum input token limit
	InputTokenLimit int `json:"inputTokenLimit,omitempty"`
	// OutputTokenLimit is the maximum output token limit
	OutputTokenLimit int `json:"outputTokenLimit,omitempty"`
	// SupportedGenerationMethods lists supported generation methods
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
	// ContextLength is the context window size
	ContextLength int `json:"context_length,omitempty"`
	// MaxCompletionTokens is the maximum completion tokens
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// SupportedParameters lists supported parameters
	SupportedParameters []string `json:"supported_parameters,omitempty"`
}

// ModelRegistration tracks a model's availability
type ModelRegistration struct {
	// Info contains the model metadata
	Info *ModelInfo
	// Count is the number of active clients that can provide this model
	Count int
	// LastUpdated tracks when this registration was last modified
	LastUpdated time.Time
	// QuotaExceededClients tracks which clients have exceeded quota for this model
	QuotaExceededClients map[string]*time.Time
}

// ModelRegistry manages the global registry of available models
type ModelRegistry struct {
	// models maps model ID to registration information
	models map[string]*ModelRegistration
	// clientModels maps client ID to the models it provides
	clientModels map[string][]string
	// mutex ensures thread-safe access to the registry
	mutex *sync.RWMutex
}

// Global model registry instance
var globalRegistry *ModelRegistry
var registryOnce sync.Once

// GetGlobalRegistry returns the global model registry instance
func GetGlobalRegistry() *ModelRegistry {
	registryOnce.Do(func() {
		globalRegistry = &ModelRegistry{
			models:       make(map[string]*ModelRegistration),
			clientModels: make(map[string][]string),
			mutex:        &sync.RWMutex{},
		}
	})
	return globalRegistry
}

// RegisterClient registers a client and its supported models
// Parameters:
//   - clientID: Unique identifier for the client
//   - clientProvider: Provider name (e.g., "gemini", "claude", "openai")
//   - models: List of models that this client can provide
func (r *ModelRegistry) RegisterClient(clientID, clientProvider string, models []*ModelInfo) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// Remove any existing registration for this client
	r.unregisterClientInternal(clientID)

	modelIDs := make([]string, 0, len(models))
	now := time.Now()

	for _, model := range models {
		modelIDs = append(modelIDs, model.ID)

		if existing, exists := r.models[model.ID]; exists {
			// Model already exists, increment count
			existing.Count++
			existing.LastUpdated = now
			log.Debugf("Incremented count for model %s, now %d clients", model.ID, existing.Count)
		} else {
			// New model, create registration
			r.models[model.ID] = &ModelRegistration{
				Info:                 model,
				Count:                1,
				LastUpdated:          now,
				QuotaExceededClients: make(map[string]*time.Time),
			}
			log.Debugf("Registered new model %s from provider %s", model.ID, clientProvider)
		}
	}

	r.clientModels[clientID] = modelIDs
	log.Debugf("Registered client %s from provider %s with %d models", clientID, clientProvider, len(models))
}

// UnregisterClient removes a client and decrements counts for its models
// Parameters:
//   - clientID: Unique identifier for the client to remove
func (r *ModelRegistry) UnregisterClient(clientID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.unregisterClientInternal(clientID)
}

// unregisterClientInternal performs the actual client unregistration (internal, no locking)
func (r *ModelRegistry) unregisterClientInternal(clientID string) {
	models, exists := r.clientModels[clientID]
	if !exists {
		return
	}

	now := time.Now()
	for _, modelID := range models {
		if registration, isExists := r.models[modelID]; isExists {
			registration.Count--
			registration.LastUpdated = now

			// Remove quota tracking for this client
			delete(registration.QuotaExceededClients, clientID)

			log.Debugf("Decremented count for model %s, now %d clients", modelID, registration.Count)

			// Remove model if no clients remain
			if registration.Count <= 0 {
				delete(r.models, modelID)
				log.Debugf("Removed model %s as no clients remain", modelID)
			}
		}
	}

	delete(r.clientModels, clientID)
	log.Debugf("Unregistered client %s", clientID)
}

// SetModelQuotaExceeded marks a model as quota exceeded for a specific client
// Parameters:
//   - clientID: The client that exceeded quota
//   - modelID: The model that exceeded quota
func (r *ModelRegistry) SetModelQuotaExceeded(clientID, modelID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if registration, exists := r.models[modelID]; exists {
		now := time.Now()
		registration.QuotaExceededClients[clientID] = &now
		log.Debugf("Marked model %s as quota exceeded for client %s", modelID, clientID)
	}
}

// ClearModelQuotaExceeded removes quota exceeded status for a model and client
// Parameters:
//   - clientID: The client to clear quota status for
//   - modelID: The model to clear quota status for
func (r *ModelRegistry) ClearModelQuotaExceeded(clientID, modelID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if registration, exists := r.models[modelID]; exists {
		delete(registration.QuotaExceededClients, clientID)
		log.Debugf("Cleared quota exceeded status for model %s and client %s", modelID, clientID)
	}
}

// GetAvailableModels returns all models that have at least one available client
// Parameters:
//   - handlerType: The handler type to filter models for (e.g., "openai", "claude", "gemini")
//
// Returns:
//   - []map[string]any: List of available models in the requested format
func (r *ModelRegistry) GetAvailableModels(handlerType string) []map[string]any {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	models := make([]map[string]any, 0)
	quotaExpiredDuration := 5 * time.Minute

	for _, registration := range r.models {
		// Check if model has any non-quota-exceeded clients
		availableClients := registration.Count
		now := time.Now()

		// Count clients that have exceeded quota but haven't recovered yet
		expiredClients := 0
		for _, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) < quotaExpiredDuration {
				expiredClients++
			}
		}

		effectiveClients := availableClients - expiredClients

		// Only include models that have available clients
		if effectiveClients > 0 {
			model := r.convertModelToMap(registration.Info, handlerType)
			if model != nil {
				models = append(models, model)
			}
		}
	}

	return models
}

// GetModelCount returns the number of available clients for a specific model
// Parameters:
//   - modelID: The model ID to check
//
// Returns:
//   - int: Number of available clients for the model
func (r *ModelRegistry) GetModelCount(modelID string) int {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if registration, exists := r.models[modelID]; exists {
		now := time.Now()
		quotaExpiredDuration := 5 * time.Minute

		// Count clients that have exceeded quota but haven't recovered yet
		expiredClients := 0
		for _, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) < quotaExpiredDuration {
				expiredClients++
			}
		}

		return registration.Count - expiredClients
	}
	return 0
}

// convertModelToMap converts ModelInfo to the appropriate format for different handler types
func (r *ModelRegistry) convertModelToMap(model *ModelInfo, handlerType string) map[string]any {
	if model == nil {
		return nil
	}

	switch handlerType {
	case "openai":
		result := map[string]any{
			"id":       model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.ContextLength > 0 {
			result["context_length"] = model.ContextLength
		}
		if model.MaxCompletionTokens > 0 {
			result["max_completion_tokens"] = model.MaxCompletionTokens
		}
		if len(model.SupportedParameters) > 0 {
			result["supported_parameters"] = model.SupportedParameters
		}
		return result

	case "claude":
		result := map[string]any{
			"id":       model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		return result

	case "gemini":
		result := map[string]any{}
		if model.Name != "" {
			result["name"] = model.Name
		} else {
			result["name"] = model.ID
		}
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.DisplayName != "" {
			result["displayName"] = model.DisplayName
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.InputTokenLimit > 0 {
			result["inputTokenLimit"] = model.InputTokenLimit
		}
		if model.OutputTokenLimit > 0 {
			result["outputTokenLimit"] = model.OutputTokenLimit
		}
		if len(model.SupportedGenerationMethods) > 0 {
			result["supportedGenerationMethods"] = model.SupportedGenerationMethods
		}
		return result

	default:
		// Generic format
		result := map[string]any{
			"id":     model.ID,
			"object": "model",
		}
		if model.OwnedBy != "" {
			result["owned_by"] = model.OwnedBy
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		return result
	}
}

// CleanupExpiredQuotas removes expired quota tracking entries
func (r *ModelRegistry) CleanupExpiredQuotas() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	now := time.Now()
	quotaExpiredDuration := 5 * time.Minute

	for modelID, registration := range r.models {
		for clientID, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) >= quotaExpiredDuration {
				delete(registration.QuotaExceededClients, clientID)
				log.Debugf("Cleaned up expired quota tracking for model %s, client %s", modelID, clientID)
			}
		}
	}
}
