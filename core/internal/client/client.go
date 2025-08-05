// Package client provides HTTP client functionality for interacting with Google Cloud AI APIs.
// It handles OAuth2 authentication, token management, request/response processing,
// streaming communication, quota management, and automatic model fallback.
// The package supports both direct API key authentication and OAuth2 flows.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
)

const (
	codeAssistEndpoint = "https://cloudcode-pa.googleapis.com"
	apiVersion         = "v1internal"
	pluginVersion      = "0.1.9"

	glEndPoint   = "https://generativelanguage.googleapis.com"
	glAPIVersion = "v1beta"
)

var (
	previewModels = map[string][]string{
		"gemini-2.5-pro":   {"gemini-2.5-pro-preview-05-06", "gemini-2.5-pro-preview-06-05"},
		"gemini-2.5-flash": {"gemini-2.5-flash-preview-04-17", "gemini-2.5-flash-preview-05-20"},
	}
)

// Client is the main client for interacting with the CLI API.
type Client struct {
	httpClient         *http.Client
	RequestMutex       sync.Mutex
	tokenStorage       *auth.TokenStorage
	cfg                *config.Config
	modelQuotaExceeded map[string]*time.Time
	glAPIKey           string
}

// NewClient creates a new CLI API client.
func NewClient(httpClient *http.Client, ts *auth.TokenStorage, cfg *config.Config, glAPIKey ...string) *Client {
	var glKey string
	if len(glAPIKey) > 0 {
		glKey = glAPIKey[0]
	}
	return &Client{
		httpClient:         httpClient,
		tokenStorage:       ts,
		cfg:                cfg,
		modelQuotaExceeded: make(map[string]*time.Time),
		glAPIKey:           glKey,
	}
}

// SetProjectID updates the project ID for the client's token storage.
func (c *Client) SetProjectID(projectID string) {
	c.tokenStorage.ProjectID = projectID
}

// SetIsAuto configures whether the client should operate in automatic mode.
func (c *Client) SetIsAuto(auto bool) {
	c.tokenStorage.Auto = auto
}

// SetIsChecked sets the checked status for the client's token storage.
func (c *Client) SetIsChecked(checked bool) {
	c.tokenStorage.Checked = checked
}

// IsChecked returns whether the client's token storage has been checked.
func (c *Client) IsChecked() bool {
	return c.tokenStorage.Checked
}

// IsAuto returns whether the client is operating in automatic mode.
func (c *Client) IsAuto() bool {
	return c.tokenStorage.Auto
}

// GetEmail returns the email address associated with the client's token storage.
func (c *Client) GetEmail() string {
	return c.tokenStorage.Email
}

// GetProjectID returns the Google Cloud project ID from the client's token storage.
func (c *Client) GetProjectID() string {
	if c.tokenStorage != nil {
		return c.tokenStorage.ProjectID
	}
	return ""
}

// GetGenerativeLanguageAPIKey returns the generative language API key if configured.
func (c *Client) GetGenerativeLanguageAPIKey() string {
	return c.glAPIKey
}

// SetupUser performs the initial user onboarding and setup.
func (c *Client) SetupUser(ctx context.Context, email, projectID string) error {
	c.tokenStorage.Email = email
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
	//
	// a, _ = json.Marshal(loadAssistReqBody)
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

	for {
		var lroResp map[string]interface{}
		err = c.makeAPIRequest(ctx, "onboardUser", "POST", onboardReqBody, &lroResp)
		if err != nil {
			return fmt.Errorf("failed to start user onboarding: %w", err)
		}
		// a, _ := json.Marshal(&lroResp)
		// log.Debug(string(a))

		// 3. Poll Long-Running Operation (LRO)
		done, doneOk := lroResp["done"].(bool)
		if doneOk && done {
			if project, projectOk := lroResp["response"].(map[string]interface{})["cloudaicompanionProject"].(map[string]interface{}); projectOk {
				if projectID != "" {
					c.tokenStorage.ProjectID = projectID
				} else {
					c.tokenStorage.ProjectID = project["id"].(string)
				}
				log.Infof("Onboarding complete. Using Project ID: %s", c.tokenStorage.ProjectID)
				return nil
			}
		} else {
			log.Println("Onboarding in progress, waiting 5 seconds...")
			time.Sleep(5 * time.Second)
		}
	}
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
	req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
	req.Header.Set("Client-Metadata", metadataStr)
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer func() {
		if err = resp.Body.Close(); err != nil {
			log.Printf("warn: failed to close response body: %v", err)
		}
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

// APIRequest handles making requests to the CLI API endpoints.
func (c *Client) APIRequest(ctx context.Context, endpoint string, body interface{}, alt string, stream bool) (io.ReadCloser, *ErrorMessage) {
	var jsonBody []byte
	var err error
	if byteBody, ok := body.([]byte); ok {
		jsonBody = byteBody
	} else {
		jsonBody, err = json.Marshal(body)
		if err != nil {
			return nil, &ErrorMessage{500, fmt.Errorf("failed to marshal request body: %w", err)}
		}
	}

	var url string
	if c.glAPIKey == "" {
		// Add alt=sse for streaming
		url = fmt.Sprintf("%s/%s:%s", codeAssistEndpoint, apiVersion, endpoint)
		if alt == "" && stream {
			url = url + "?alt=sse"
		} else {
			if alt != "" {
				url = url + fmt.Sprintf("?$alt=%s", alt)
			}
		}
	} else {
		if endpoint == "countTokens" {
			modelResult := gjson.GetBytes(jsonBody, "model")
			url = fmt.Sprintf("%s/%s/models/%s:%s", glEndPoint, glAPIVersion, modelResult.String(), endpoint)
		} else {
			modelResult := gjson.GetBytes(jsonBody, "model")
			url = fmt.Sprintf("%s/%s/models/%s:%s", glEndPoint, glAPIVersion, modelResult.String(), endpoint)
			if alt == "" && stream {
				url = url + "?alt=sse"
			} else {
				if alt != "" {
					url = url + fmt.Sprintf("?$alt=%s", alt)
				}
			}
			jsonBody = []byte(gjson.GetBytes(jsonBody, "request").Raw)
			systemInstructionResult := gjson.GetBytes(jsonBody, "systemInstruction")
			if systemInstructionResult.Exists() {
				jsonBody, _ = sjson.SetRawBytes(jsonBody, "system_instruction", []byte(systemInstructionResult.Raw))
				jsonBody, _ = sjson.DeleteBytes(jsonBody, "systemInstruction")
				jsonBody, _ = sjson.DeleteBytes(jsonBody, "session_id")
			}
		}
	}

	// log.Debug(string(jsonBody))
	// log.Debug(url)
	reqBody := bytes.NewBuffer(jsonBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, reqBody)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to create request: %v", err)}
	}

	// Set headers
	metadataStr := getClientMetadataString()
	req.Header.Set("Content-Type", "application/json")
	if c.glAPIKey == "" {
		token, errToken := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
		if errToken != nil {
			return nil, &ErrorMessage{500, fmt.Errorf("failed to get token: %v", errToken)}
		}
		req.Header.Set("User-Agent", getUserAgent())
		req.Header.Set("X-Goog-Api-Client", "gl-node/22.17.0")
		req.Header.Set("Client-Metadata", metadataStr)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	} else {
		req.Header.Set("x-goog-api-key", c.glAPIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &ErrorMessage{500, fmt.Errorf("failed to execute request: %v", err)}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() {
			if err = resp.Body.Close(); err != nil {
				log.Printf("warn: failed to close response body: %v", err)
			}
		}()
		bodyBytes, _ := io.ReadAll(resp.Body)
		// log.Debug(string(jsonBody))
		return nil, &ErrorMessage{resp.StatusCode, fmt.Errorf(string(bodyBytes))}
	}

	return resp.Body, nil
}

// SendMessage handles a single conversational turn, including tool calls.
func (c *Client) SendMessage(ctx context.Context, rawJSON []byte, model string, systemInstruction *Content, contents []Content, tools []ToolDeclaration) ([]byte, *ErrorMessage) {
	request := GenerateContentRequest{
		Contents: contents,
		GenerationConfig: GenerationConfig{
			ThinkingConfig: GenerationConfigThinkingConfig{
				IncludeThoughts: true,
			},
		},
	}

	request.SystemInstruction = systemInstruction

	request.Tools = tools

	requestBody := map[string]interface{}{
		"project": c.GetProjectID(), // Assuming ProjectID is available
		"request": request,
		"model":   model,
	}

	byteRequestBody, _ := json.Marshal(requestBody)

	// log.Debug(string(byteRequestBody))

	reasoningEffortResult := gjson.GetBytes(rawJSON, "reasoning_effort")
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

	temperatureResult := gjson.GetBytes(rawJSON, "temperature")
	if temperatureResult.Exists() && temperatureResult.Type == gjson.Number {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.temperature", temperatureResult.Num)
	}

	topPResult := gjson.GetBytes(rawJSON, "top_p")
	if topPResult.Exists() && topPResult.Type == gjson.Number {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topP", topPResult.Num)
	}

	topKResult := gjson.GetBytes(rawJSON, "top_k")
	if topKResult.Exists() && topKResult.Type == gjson.Number {
		byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topK", topKResult.Num)
	}

	modelName := model
	// log.Debug(string(byteRequestBody))
	for {
		if c.isModelQuotaExceeded(modelName) {
			if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
				modelName = c.getPreviewModel(model)
				if modelName != "" {
					log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
					byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "model", modelName)
					continue
				}
			}
			return nil, &ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
			}
		}

		respBody, err := c.APIRequest(ctx, "generateContent", byteRequestBody, "", false)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					continue
				}
			}
			return nil, err
		}
		delete(c.modelQuotaExceeded, modelName)
		bodyBytes, errReadAll := io.ReadAll(respBody)
		if errReadAll != nil {
			return nil, &ErrorMessage{StatusCode: 500, Error: errReadAll}
		}
		return bodyBytes, nil
	}
}

// SendMessageStream handles streaming conversational turns with comprehensive parameter management.
// This function implements a sophisticated streaming system that supports tool calls, reasoning modes,
// quota management, and automatic model fallback. It returns two channels for asynchronous communication:
// one for streaming response data and another for error handling.
func (c *Client) SendMessageStream(ctx context.Context, rawJSON []byte, model string, systemInstruction *Content, contents []Content, tools []ToolDeclaration, includeThoughts ...bool) (<-chan []byte, <-chan *ErrorMessage) {
	// Define the data prefix used in Server-Sent Events streaming format
	dataTag := []byte("data: ")

	// Create channels for asynchronous communication
	// errChan: delivers error messages during streaming
	// dataChan: delivers response data chunks
	errChan := make(chan *ErrorMessage)
	dataChan := make(chan []byte)

	// Launch a goroutine to handle the streaming process asynchronously
	// This allows the function to return immediately while processing continues in the background
	go func() {
		// Ensure channels are properly closed when the goroutine exits
		defer close(errChan)
		defer close(dataChan)

		// Configure thinking/reasoning capabilities
		// Default to including thoughts unless explicitly disabled
		includeThoughtsFlag := true
		if len(includeThoughts) > 0 {
			includeThoughtsFlag = includeThoughts[0]
		}

		// Build the base request structure for the Gemini API
		// This includes conversation contents and generation configuration
		request := GenerateContentRequest{
			Contents: contents,
			GenerationConfig: GenerationConfig{
				ThinkingConfig: GenerationConfigThinkingConfig{
					IncludeThoughts: includeThoughtsFlag,
				},
			},
		}

		// Add system instructions if provided
		// System instructions guide the AI's behavior and response style
		request.SystemInstruction = systemInstruction

		// Add available tools for function calling capabilities
		// Tools allow the AI to perform actions beyond text generation
		request.Tools = tools

		// Construct the complete request body with project context
		// The project ID is essential for proper API routing and billing
		requestBody := map[string]interface{}{
			"project": c.GetProjectID(), // Project ID for API routing and quota management
			"request": request,
			"model":   model,
		}

		// Serialize the request body to JSON for API transmission
		byteRequestBody, _ := json.Marshal(requestBody)

		// Parse and configure reasoning effort levels from the original request
		// This maps Claude-style reasoning effort parameters to Gemini's thinking budget system
		reasoningEffortResult := gjson.GetBytes(rawJSON, "reasoning_effort")
		if reasoningEffortResult.String() == "none" {
			// Disable thinking entirely for fastest responses
			byteRequestBody, _ = sjson.DeleteBytes(byteRequestBody, "request.generationConfig.thinkingConfig.include_thoughts")
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 0)
		} else if reasoningEffortResult.String() == "auto" {
			// Let the model decide the appropriate thinking budget automatically
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		} else if reasoningEffortResult.String() == "low" {
			// Minimal thinking for simple tasks (1KB thinking budget)
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 1024)
		} else if reasoningEffortResult.String() == "medium" {
			// Moderate thinking for complex tasks (8KB thinking budget)
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 8192)
		} else if reasoningEffortResult.String() == "high" {
			// Maximum thinking for very complex tasks (24KB thinking budget)
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", 24576)
		} else {
			// Default to automatic thinking budget if no specific level is provided
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
		}

		// Configure temperature parameter for response randomness control
		// Temperature affects the creativity vs consistency trade-off in responses
		temperatureResult := gjson.GetBytes(rawJSON, "temperature")
		if temperatureResult.Exists() && temperatureResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.temperature", temperatureResult.Num)
		}

		// Configure top-p parameter for nucleus sampling
		// Controls the cumulative probability threshold for token selection
		topPResult := gjson.GetBytes(rawJSON, "top_p")
		if topPResult.Exists() && topPResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topP", topPResult.Num)
		}

		// Configure top-k parameter for limiting token candidates
		// Restricts the model to consider only the top K most likely tokens
		topKResult := gjson.GetBytes(rawJSON, "top_k")
		if topKResult.Exists() && topKResult.Type == gjson.Number {
			byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "request.generationConfig.topK", topKResult.Num)
		}

		// Initialize model name for quota management and potential fallback
		modelName := model
		var stream io.ReadCloser

		// Quota management and model fallback loop
		// This loop handles quota exceeded scenarios and automatic model switching
		for {
			// Check if the current model has exceeded its quota
			if c.isModelQuotaExceeded(modelName) {
				// Attempt to switch to a preview model if configured and using account auth
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					modelName = c.getPreviewModel(model)
					if modelName != "" {
						log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
						// Update the request body with the new model name
						byteRequestBody, _ = sjson.SetBytes(byteRequestBody, "model", modelName)
						continue // Retry with the preview model
					}
				}
				// If no fallback is available, return a quota exceeded error
				errChan <- &ErrorMessage{
					StatusCode: 429,
					Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
				}
				return
			}

			// Attempt to establish a streaming connection with the API
			var err *ErrorMessage
			stream, err = c.APIRequest(ctx, "streamGenerateContent", byteRequestBody, "", true)
			if err != nil {
				// Handle quota exceeded errors by marking the model and potentially retrying
				if err.StatusCode == 429 {
					now := time.Now()
					c.modelQuotaExceeded[modelName] = &now // Mark model as quota exceeded
					// If preview model switching is enabled, retry the loop
					if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
						continue
					}
				}
				// Forward other errors to the error channel
				errChan <- err
				return
			}
			// Clear any previous quota exceeded status for this model
			delete(c.modelQuotaExceeded, modelName)
			break // Successfully established connection, exit the retry loop
		}

		// Process the streaming response using a scanner
		// This handles the Server-Sent Events format from the API
		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := scanner.Bytes()
			// Filter and forward only data lines (those prefixed with "data: ")
			// This extracts the actual JSON content from the SSE format
			if bytes.HasPrefix(line, dataTag) {
				dataChan <- line[6:] // Remove "data: " prefix and send the JSON content
			}
		}

		// Handle any scanning errors that occurred during stream processing
		if errScanner := scanner.Err(); errScanner != nil {
			// Send a 500 Internal Server Error for scanning failures
			errChan <- &ErrorMessage{500, errScanner}
			_ = stream.Close()
			return
		}

		// Ensure the stream is properly closed to prevent resource leaks
		_ = stream.Close()
	}()

	// Return the channels immediately for asynchronous communication
	// The caller can read from these channels while the goroutine processes the request
	return dataChan, errChan
}

// SendRawTokenCount handles a token count.
func (c *Client) SendRawTokenCount(ctx context.Context, rawJSON []byte, alt string) ([]byte, *ErrorMessage) {
	modelResult := gjson.GetBytes(rawJSON, "model")
	model := modelResult.String()
	modelName := model
	for {
		if c.isModelQuotaExceeded(modelName) {
			if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
				modelName = c.getPreviewModel(model)
				if modelName != "" {
					log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
					rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelName)
					continue
				}
			}
			return nil, &ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
			}
		}

		respBody, err := c.APIRequest(ctx, "countTokens", rawJSON, alt, false)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					continue
				}
			}
			return nil, err
		}
		delete(c.modelQuotaExceeded, modelName)
		bodyBytes, errReadAll := io.ReadAll(respBody)
		if errReadAll != nil {
			return nil, &ErrorMessage{StatusCode: 500, Error: errReadAll}
		}
		return bodyBytes, nil
	}
}

// SendRawMessage handles a single conversational turn, including tool calls.
func (c *Client) SendRawMessage(ctx context.Context, rawJSON []byte, alt string) ([]byte, *ErrorMessage) {
	if c.glAPIKey == "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "project", c.GetProjectID())
	}

	modelResult := gjson.GetBytes(rawJSON, "model")
	model := modelResult.String()
	modelName := model
	for {
		if c.isModelQuotaExceeded(modelName) {
			if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
				modelName = c.getPreviewModel(model)
				if modelName != "" {
					log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
					rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelName)
					continue
				}
			}
			return nil, &ErrorMessage{
				StatusCode: 429,
				Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
			}
		}

		respBody, err := c.APIRequest(ctx, "generateContent", rawJSON, alt, false)
		if err != nil {
			if err.StatusCode == 429 {
				now := time.Now()
				c.modelQuotaExceeded[modelName] = &now
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					continue
				}
			}
			return nil, err
		}
		delete(c.modelQuotaExceeded, modelName)
		bodyBytes, errReadAll := io.ReadAll(respBody)
		if errReadAll != nil {
			return nil, &ErrorMessage{StatusCode: 500, Error: errReadAll}
		}
		return bodyBytes, nil
	}
}

// SendRawMessageStream handles a single conversational turn, including tool calls.
func (c *Client) SendRawMessageStream(ctx context.Context, rawJSON []byte, alt string) (<-chan []byte, <-chan *ErrorMessage) {
	dataTag := []byte("data: ")
	errChan := make(chan *ErrorMessage)
	dataChan := make(chan []byte)
	go func() {
		defer close(errChan)
		defer close(dataChan)

		if c.glAPIKey == "" {
			rawJSON, _ = sjson.SetBytes(rawJSON, "project", c.GetProjectID())
		}

		modelResult := gjson.GetBytes(rawJSON, "model")
		model := modelResult.String()
		modelName := model
		var stream io.ReadCloser
		for {
			if c.isModelQuotaExceeded(modelName) {
				if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
					modelName = c.getPreviewModel(model)
					if modelName != "" {
						log.Debugf("Model %s is quota exceeded. Switch to preview model %s", model, modelName)
						rawJSON, _ = sjson.SetBytes(rawJSON, "model", modelName)
						continue
					}
				}
				errChan <- &ErrorMessage{
					StatusCode: 429,
					Error:      fmt.Errorf(`{"error":{"code":429,"message":"All the models of '%s' are quota exceeded","status":"RESOURCE_EXHAUSTED"}}`, model),
				}
				return
			}
			var err *ErrorMessage
			stream, err = c.APIRequest(ctx, "streamGenerateContent", rawJSON, alt, true)
			if err != nil {
				if err.StatusCode == 429 {
					now := time.Now()
					c.modelQuotaExceeded[modelName] = &now
					if c.cfg.QuotaExceeded.SwitchPreviewModel && c.glAPIKey == "" {
						continue
					}
				}
				errChan <- err
				return
			}
			delete(c.modelQuotaExceeded, modelName)
			break
		}

		if alt == "" {
			scanner := bufio.NewScanner(stream)
			for scanner.Scan() {
				line := scanner.Bytes()
				if bytes.HasPrefix(line, dataTag) {
					dataChan <- line[6:]
				}
			}

			if errScanner := scanner.Err(); errScanner != nil {
				errChan <- &ErrorMessage{500, errScanner}
				_ = stream.Close()
				return
			}

		} else {
			data, err := io.ReadAll(stream)
			if err != nil {
				errChan <- &ErrorMessage{500, err}
				_ = stream.Close()
				return
			}
			dataChan <- data
		}
		_ = stream.Close()

	}()

	return dataChan, errChan
}

// isModelQuotaExceeded checks if the specified model has exceeded its quota
// within the last 30 minutes.
func (c *Client) isModelQuotaExceeded(model string) bool {
	if lastExceededTime, hasKey := c.modelQuotaExceeded[model]; hasKey {
		duration := time.Now().Sub(*lastExceededTime)
		if duration > 30*time.Minute {
			return false
		}
		return true
	}
	return false
}

// getPreviewModel returns an available preview model for the given base model,
// or an empty string if no preview models are available or all are quota exceeded.
func (c *Client) getPreviewModel(model string) string {
	if models, hasKey := previewModels[model]; hasKey {
		for i := 0; i < len(models); i++ {
			if !c.isModelQuotaExceeded(models[i]) {
				return models[i]
			}
		}
	}
	return ""
}

// IsModelQuotaExceeded returns true if the specified model has exceeded its quota
// and no fallback options are available.
func (c *Client) IsModelQuotaExceeded(model string) bool {
	if c.isModelQuotaExceeded(model) {
		if c.cfg.QuotaExceeded.SwitchPreviewModel {
			return c.getPreviewModel(model) == ""
		}
		return true
	}
	return false
}

// CheckCloudAPIIsEnabled sends a simple test request to the API to verify
// that the Cloud AI API is enabled for the user's project. It provides
// an activation URL if the API is disabled.
func (c *Client) CheckCloudAPIIsEnabled() (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		c.RequestMutex.Unlock()
		cancel()
	}()
	c.RequestMutex.Lock()

	// A simple request to test the API endpoint.
	requestBody := fmt.Sprintf(`{"project":"%s","request":{"contents":[{"role":"user","parts":[{"text":"Be concise. What is the capital of France?"}]}],"generationConfig":{"thinkingConfig":{"include_thoughts":false,"thinkingBudget":0}}},"model":"gemini-2.5-flash"}`, c.tokenStorage.ProjectID)

	stream, err := c.APIRequest(ctx, "streamGenerateContent", []byte(requestBody), "", true)
	if err != nil {
		// If a 403 Forbidden error occurs, it likely means the API is not enabled.
		if err.StatusCode == 403 {
			errJSON := err.Error.Error()
			// Check for a specific error code and extract the activation URL.
			if gjson.Get(errJSON, "0.error.code").Int() == 403 {
				activationURL := gjson.Get(errJSON, "0.error.details.0.metadata.activationUrl").String()
				if activationURL != "" {
					log.Warnf(
						"\n\nPlease activate your account with this url:\n\n%s\n\n And execute this command again:\n%s --login --project_id %s",
						activationURL,
						os.Args[0],
						c.tokenStorage.ProjectID,
					)
				}
			}
			log.Warnf("\n\nPlease copy this message and create an issue.\n\n%s\n\n", errJSON)
			return false, nil
		}
		return false, err.Error
	}
	defer func() {
		_ = stream.Close()
	}()

	// We only need to know if the request was successful, so we can drain the stream.
	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		// Do nothing, just consume the stream.
	}

	return scanner.Err() == nil, scanner.Err()
}

// GetProjectList fetches a list of Google Cloud projects accessible by the user.
func (c *Client) GetProjectList(ctx context.Context) (*GCPProject, error) {
	token, err := c.httpClient.Transport.(*oauth2.Transport).Source.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if err != nil {
		return nil, fmt.Errorf("could not create project list request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var project GCPProject
	if err = json.NewDecoder(resp.Body).Decode(&project); err != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", err)
	}
	return &project, nil
}

// SaveTokenToFile serializes the client's current token storage to a JSON file.
// The filename is constructed from the user's email and project ID.
func (c *Client) SaveTokenToFile() error {
	if err := os.MkdirAll(c.cfg.AuthDir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	fileName := filepath.Join(c.cfg.AuthDir, fmt.Sprintf("%s-%s.json", c.tokenStorage.Email, c.tokenStorage.ProjectID))
	log.Infof("Saving credentials to %s", fileName)
	f, err := os.Create(fileName)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(c.tokenStorage); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}

// getClientMetadata returns a map of metadata about the client environment,
// such as IDE type, platform, and plugin version.
func getClientMetadata() map[string]string {
	return map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
		// "pluginVersion": pluginVersion,
	}
}

// getClientMetadataString returns the client metadata as a single,
// comma-separated string, which is required for the 'Client-Metadata' header.
func getClientMetadataString() string {
	md := getClientMetadata()
	parts := make([]string, 0, len(md))
	for k, v := range md {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// getUserAgent constructs the User-Agent string for HTTP requests.
func getUserAgent() string {
	// return fmt.Sprintf("GeminiCLI/%s (%s; %s)", pluginVersion, runtime.GOOS, runtime.GOARCH)
	return "google-api-nodejs-client/9.15.1"
}

// getPlatform determines the operating system and architecture and formats
// it into a string expected by the backend API.
func getPlatform() string {
	goOS := runtime.GOOS
	arch := runtime.GOARCH
	switch goOS {
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
