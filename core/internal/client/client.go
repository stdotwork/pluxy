package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"
)

// --- Constants ---
const (
	codeAssistEndpoint = "https://cloudcode-pa.googleapis.com"
	apiVersion         = "v1internal"
	pluginVersion      = "1.0.0"
)

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

// Client is the main client for interacting with the CLI API.
type Client struct {
	httpClient   *http.Client
	projectID    string
	RequestMutex sync.Mutex
	Email        string
}

// NewClient creates a new CLI API client.
func NewClient(httpClient *http.Client) *Client {
	return &Client{
		httpClient: httpClient,
	}
}

// SetupUser performs the initial user onboarding and setup.
func (c *Client) SetupUser(ctx context.Context, email, projectID string) error {
	c.Email = email
	log.Info("Performing user onboarding...")

	// 1. LoadCodeAssist
	loadAssistReqBody := map[string]interface{}{
		"metadata": getClientMetadata(),
	}
	if projectID != "" {
		loadAssistReqBody["cloudaicompanionProject"] = projectID
	}

	var loadAssistResp map[string]interface{}
	err := c.makeAPIRequest(ctx, "loadCodeAssist", "POST", loadAssistReqBody, &loadAssistResp)
	if err != nil {
		return fmt.Errorf("failed to load code assist: %w", err)
	}

	// a, _ := json.Marshal(&loadAssistResp)
	// log.Debug(string(a))

	// 2. OnboardUser
	var onboardTierID = "legacy-tier"
	if tiers, ok := loadAssistResp["allowedTiers"].([]interface{}); ok {
		for _, t := range tiers {
			if tier, tierOk := t.(map[string]interface{}); tierOk {
				if isDefault, isDefaultOk := tier["isDefault"].(bool); isDefaultOk && isDefault {
					if id, idOk := tier["id"].(string); idOk {
						onboardTierID = id
						break
					}
				}
			}
		}
	}

	onboardProjectID := projectID
	if p, ok := loadAssistResp["cloudaicompanionProject"].(string); ok && p != "" {
		onboardProjectID = p
	}

	onboardReqBody := map[string]interface{}{
		"tierId":   onboardTierID,
		"metadata": getClientMetadata(),
	}
	if onboardProjectID != "" {
		onboardReqBody["cloudaicompanionProject"] = onboardProjectID
	} else {
		return fmt.Errorf("failed to start user onboarding, need define a project id")
	}

	var lroResp map[string]interface{}
	err = c.makeAPIRequest(ctx, "onboardUser", "POST", onboardReqBody, &lroResp)
	if err != nil {
		return fmt.Errorf("failed to start user onboarding: %w", err)
	}

	// a, _ = json.Marshal(&lroResp)
	// log.Debug(string(a))

	// 3. Poll Long-Running Operation (LRO)
	if done, doneOk := lroResp["done"].(bool); doneOk && done {
		if project, projectOk := lroResp["response"].(map[string]interface{})["cloudaicompanionProject"].(map[string]interface{}); projectOk {
			c.projectID = project["id"].(string)
			log.Infof("Onboarding complete. Using Project ID: %s", c.projectID)
			return nil
		}
	}
	return fmt.Errorf("failed to get operation name from onboarding response: %v", lroResp)
}

// makeAPIRequest handles making requests to the CLI API endpoints.
func (c *Client) makeAPIRequest(ctx context.Context, endpoint, method string, body interface{}, result interface{}) error {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	url := fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, apiVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		url = fmt.Sprintf("%s/%s", codeAssistEndpoint, endpoint)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	// Set headers
	metadataStr := getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", getUserAgent())
	req.Header.Set("Client-Metadata", metadataStr)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if result != nil {
		if err = json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("failed to decode response body: %w", err)
		}
	}

	return nil
}

// StreamAPIRequest handles making streaming requests to the CLI API endpoints.
func (c *Client) StreamAPIRequest(ctx context.Context, endpoint string, body interface{}) (io.ReadCloser, error) {
	var jsonBody []byte
	var err error
	if byteBody, ok := body.([]byte); ok {
		jsonBody = byteBody
	} else {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
	}
	// log.Debug(string(jsonBody))
	reqBody := bytes.NewBuffer(jsonBody)

	// Add alt=sse for streaming
	url := fmt.Sprintf("%s/%s:%s?alt=sse", codeAssistEndpoint, apiVersion, endpoint)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	// Set headers
	metadataStr := getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", getUserAgent())
	req.Header.Set("Client-Metadata", metadataStr)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			_ = resp.Body.Close()
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("api streaming request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return resp.Body, nil
}

// SendMessageStream handles a single conversational turn, including tool calls.
func (c *Client) SendMessageStream(ctx context.Context, rawJson []byte, model string, contents []Content, tools []ToolDeclaration) (<-chan []byte, <-chan error) {
	dataTag := []byte("data: ")
	errChan := make(chan error)
	dataChan := make(chan []byte)
	go func() {
		defer close(errChan)
		defer close(dataChan)

		request := GenerateContentRequest{
			Contents: contents,
			GenerationConfig: GenerationConfig{
				ThinkingConfig: GenerationConfigThinkingConfig{
					IncludeThoughts: true,
				},
			},
		}
		request.Tools = tools

		requestBody := map[string]interface{}{
			"project": c.projectID, // Assuming ProjectID is available
			"request": request,
			"model":   model,
		}

		byteRequestBody, _ := json.Marshal(requestBody)

		// log.Debug(string(rawJson))

		reasoningEffortResult := gjson.GetBytes(rawJson, "reasoning_effort")
		if reasoningEffortResult.String() == "none" {
			byteRequestBody, _ = sjson.DeleteBytes(byteRequestBody, "request.generationConfig.thinkingConfig.include_thoughts")
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 0)
		} else if reasoningEffortResult.String() == "auto" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		} else if reasoningEffortResult.String() == "low" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 1024)
		} else if reasoningEffortResult.String() == "medium" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 8192)
		} else if reasoningEffortResult.String() == "high" {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 24576)
		} else {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		}

		temperatureResult := gjson.GetBytes(rawJson, "temperature")
		if temperatureResult.Exists() && temperatureResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.temperature", temperatureResult.Num)
		}

		topPResult := gjson.GetBytes(rawJson, "top_p")
		if topPResult.Exists() && topPResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topP", topPResult.Num)
		}

		topKResult := gjson.GetBytes(rawJson, "top_k")
		if topKResult.Exists() && topKResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topK", topKResult.Num)
		}

		// log.Debug(string(byteRequestBody))

		stream, err := c.StreamAPIRequest(ctx, "streamGenerateContent", byteRequestBody)
		if err != nil {
			// log.Println(err)
			errChan <- err
			return
		}

		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Bytes()
			// log.Printf("Received stream chunk: %s", line)
			if bytes.HasPrefix(line, dataTag) {
				dataChan <- line[6:]
			}
		}

		if err = scanner.Err(); err != nil {
			// log.Println(err)
			errChan <- err
			_ = stream.Close()
			return
		}

		_ = stream.Close()
	}()

	return dataChan, errChan
}

func (c *Client) GetProjectList(ctx context.Context) (*GCPProject, error) {
	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("could not get project list: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("get user info request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var project GCPProject
	err = json.Unmarshal(bodyBytes, &project)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", err)
	}
	return &project, nil
}

// getClientMetadata returns metadata about the client environment.
func getClientMetadata() map[string]string {
	return map[string]string{
		"ideType":       "IDE_UNSPECIFIED",
		"platform":      getPlatform(),
		"pluginType":    "GEMINI",
		"pluginVersion": pluginVersion,
	}
}

// getClientMetadataString returns the metadata as a comma-separated string.
func getClientMetadataString() string {
	md := getClientMetadata()
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

func getUserAgent() string {
	return fmt.Sprintf(fmt.Sprintf("GeminiCLI/%s (%s; %s)", pluginVersion, runtime.GOOS, runtime.GOARCH))
}

// getPlatform returns the OS and architecture in the format expected by the API.
func getPlatform() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	switch os {
	case "darwin":
		return fmt.Sprintf("DARWIN_%s", strings.ToUpper(arch))
	case "linux":
		return fmt.Sprintf("LINUX_%s", strings.ToUpper(arch))
	case "windows":
		return fmt.Sprintf("WINDOWS_%s", strings.ToUpper(arch))
	default:
		return "PLATFORM_UNSPECIFIED"
	}
}
