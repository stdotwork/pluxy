// Package util provides utility functions used across the CLIProxyAPI application.
// These functions handle common tasks such as determining AI service providers
// from model names and managing HTTP proxies.
package util

import (
	"strings"

	"github.com/luispater/CLIProxyAPI/v5/internal/config"
)

// GetProviderName determines the AI service provider based on the model name.
// It analyzes the model name string to identify which service provider it belongs to.
// First checks for OpenAI compatibility aliases, then falls back to standard provider detection.
//
// Supported providers:
//   - "gemini" for Google's Gemini models
//   - "gpt" for OpenAI's GPT models
//   - "claude" for Anthropic's Claude models
//   - "qwen" for Alibaba's Qwen models
//   - "openai-compatibility" for external OpenAI-compatible providers
//   - "unknow" for unrecognized model names
//
// Parameters:
//   - modelName: The name of the model to identify the provider for.
//   - cfg: The application configuration containing OpenAI compatibility settings.
//
// Returns:
//   - string: The name of the provider.
func GetProviderName(modelName string, cfg *config.Config) string {
	// First check if this model name is an OpenAI compatibility alias
	if IsOpenAICompatibilityAlias(modelName, cfg) {
		return "openai-compatibility"
	} else if strings.Contains(modelName, "gemini") { // Fall back to standard provider detection
		return "gemini"
	} else if strings.Contains(modelName, "gpt") {
		return "gpt"
	} else if strings.Contains(modelName, "codex") {
		return "gpt"
	} else if strings.HasPrefix(modelName, "claude") {
		return "claude"
	} else if strings.HasPrefix(modelName, "qwen") {
		return "qwen"
	}
	return "unknow"
}

// IsOpenAICompatibilityAlias checks if the given model name is an alias
// configured for OpenAI compatibility routing.
//
// Parameters:
//   - modelName: The model name to check
//   - cfg: The application configuration containing OpenAI compatibility settings
//
// Returns:
//   - bool: True if the model name is an OpenAI compatibility alias, false otherwise
func IsOpenAICompatibilityAlias(modelName string, cfg *config.Config) bool {
	if cfg == nil {
		return false
	}

	for _, compat := range cfg.OpenAICompatibility {
		for _, model := range compat.Models {
			if model.Alias == modelName {
				return true
			}
		}
	}
	return false
}

// GetOpenAICompatibilityConfig returns the OpenAI compatibility configuration
// and model details for the given alias.
//
// Parameters:
//   - alias: The model alias to find configuration for
//   - cfg: The application configuration containing OpenAI compatibility settings
//
// Returns:
//   - *config.OpenAICompatibility: The matching compatibility configuration, or nil if not found
//   - *config.OpenAICompatibilityModel: The matching model configuration, or nil if not found
func GetOpenAICompatibilityConfig(alias string, cfg *config.Config) (*config.OpenAICompatibility, *config.OpenAICompatibilityModel) {
	if cfg == nil {
		return nil, nil
	}

	for _, compat := range cfg.OpenAICompatibility {
		for _, model := range compat.Models {
			if model.Alias == alias {
				return &compat, &model
			}
		}
	}
	return nil, nil
}

// InArray checks if a string exists in a slice of strings.
// It iterates through the slice and returns true if the target string is found,
// otherwise it returns false.
//
// Parameters:
//   - hystack: The slice of strings to search in
//   - needle: The string to search for
//
// Returns:
//   - bool: True if the string is found, false otherwise
func InArray(hystack []string, needle string) bool {
	for _, item := range hystack {
		if needle == item {
			return true
		}
	}
	return false
}

// HideAPIKey obscures an API key for logging purposes, showing only the first and last few characters.
//
// Parameters:
//   - apiKey: The API key to hide.
//
// Returns:
//   - string: The obscured API key.
func HideAPIKey(apiKey string) string {
	if len(apiKey) > 8 {
		return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
	} else if len(apiKey) > 4 {
		return apiKey[:2] + "..." + apiKey[len(apiKey)-2:]
	} else if len(apiKey) > 2 {
		return apiKey[:1] + "..." + apiKey[len(apiKey)-1:]
	}
	return apiKey
}
