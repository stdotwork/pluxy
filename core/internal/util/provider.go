// Package util provides utility functions used across the CLIProxyAPI application.
// These functions handle common tasks such as determining AI service providers
// from model names and managing HTTP proxies.
package util

import (
	"strings"
)

// GetProviderName determines the AI service provider based on the model name.
// It analyzes the model name string to identify which service provider it belongs to.
//
// Supported providers:
//   - "gemini" for Google's Gemini models
//   - "gpt" for OpenAI's GPT models
//   - "claude" for Anthropic's Claude models
//   - "qwen" for Alibaba's Qwen models
//   - "unknow" for unrecognized model names
//
// Parameters:
//   - modelName: The name of the model to identify the provider for.
//
// Returns:
//   - string: The name of the provider.
func GetProviderName(modelName string) string {
	if strings.Contains(modelName, "gemini") {
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
