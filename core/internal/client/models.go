package client

import "time"

type ErrorMessage struct {
	StatusCode int
	Error      error
}

type GCPProject struct {
	Projects []GCPProjectProjects `json:"projects"`
}
type GCPProjectLabels struct {
	GenerativeLanguage string `json:"generative-language"`
}
type GCPProjectProjects struct {
	ProjectNumber  string           `json:"projectNumber"`
	ProjectID      string           `json:"projectId"`
	LifecycleState string           `json:"lifecycleState"`
	Name           string           `json:"name"`
	Labels         GCPProjectLabels `json:"labels"`
	CreateTime     time.Time        `json:"createTime"`
}

type Content struct {
	Role  string `json:"role"`
	Parts []Part `json:"parts"`
}

// Part represents a single part of a message's content.
type Part struct {
	Text             string            `json:"text,omitempty"`
	InlineData       *InlineData       `json:"inlineData,omitempty"`
	FunctionCall     *FunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`
}

type InlineData struct {
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

// FunctionCall represents a tool call requested by the model.
type FunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// FunctionResponse represents the result of a tool execution.
type FunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

// GenerateContentRequest is the request payload for the streamGenerateContent endpoint.
type GenerateContentRequest struct {
	Contents         []Content         `json:"contents"`
	Tools            []ToolDeclaration `json:"tools,omitempty"`
	GenerationConfig `json:"generationConfig"`
}

// GenerationConfig defines model generation parameters.
type GenerationConfig struct {
	ThinkingConfig GenerationConfigThinkingConfig `json:"thinkingConfig,omitempty"`
	Temperature    float64                        `json:"temperature,omitempty"`
	TopP           float64                        `json:"topP,omitempty"`
	TopK           float64                        `json:"topK,omitempty"`
	// Temperature, TopP, TopK, etc. can be added here.
}

type GenerationConfigThinkingConfig struct {
	IncludeThoughts bool `json:"include_thoughts,omitempty"`
}

// ToolDeclaration is the structure for declaring tools to the API.
// For now, we'll assume a simple structure. A more complete implementation
// would mirror the OpenAPI schema definition.
type ToolDeclaration struct {
	FunctionDeclarations []interface{} `json:"functionDeclarations"`
}
