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
// - "gemini" for Google's Gemini models
// - "gpt" for OpenAI's GPT models
// - "unknow" for unrecognized model names
func GetProviderName(modelName string) string {
	if strings.Contains(modelName, "gemini") {
		return "gemini"
	} else if strings.Contains(modelName, "gpt") {
		return "gpt"
	}
	return "unknow"
}
