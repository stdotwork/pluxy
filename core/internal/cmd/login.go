// Package cmd provides command-line interface functionality for the CLI Proxy API server.
// It includes authentication flows for various AI service providers, service startup,
// and other command-line operations.
package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	geminiCLIEndpoint       = "https://cloudcode-pa.googleapis.com"
	geminiCLIVersion        = "v1internal"
	geminiCLIUserAgent      = "google-api-nodejs-client/9.15.1"
	geminiCLIApiClient      = "gl-node/22.17.0"
	geminiCLIClientMetadata = "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
)

type projectSelectionRequiredError struct{}

func (e *projectSelectionRequiredError) Error() string {
	return "gemini cli: project selection required"
}

// DoLogin handles Google Gemini authentication using the shared authentication manager.
// It initiates the OAuth flow for Google Gemini services, performs the legacy CLI user setup,
// and saves the authentication tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - projectID: Optional Google Cloud project ID for Gemini services
//   - options: Login options including browser behavior and prompts
func DoLogin(cfg *config.Config, projectID string, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	ctx := context.Background()

	loginOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		ProjectID: strings.TrimSpace(projectID),
		Metadata:  map[string]string{},
		Prompt:    options.Prompt,
	}

	authenticator := sdkAuth.NewGeminiAuthenticator()
	record, errLogin := authenticator.Login(ctx, cfg, loginOpts)
	if errLogin != nil {
		log.Fatalf("Gemini authentication failed: %v", errLogin)
		return
	}

	storage, okStorage := record.Storage.(*gemini.GeminiTokenStorage)
	if !okStorage || storage == nil {
		log.Fatal("Gemini authentication failed: unsupported token storage")
		return
	}

	geminiAuth := gemini.NewGeminiAuth()
	httpClient, errClient := geminiAuth.GetAuthenticatedClient(ctx, storage, cfg, options.NoBrowser)
	if errClient != nil {
		log.Fatalf("Gemini authentication failed: %v", errClient)
		return
	}

	log.Info("Authentication successful.")

	if errSetup := performGeminiCLISetup(ctx, httpClient, storage, strings.TrimSpace(projectID)); errSetup != nil {
		var projectErr *projectSelectionRequiredError
		if errors.As(errSetup, &projectErr) {
			log.Error("Failed to start user onboarding: A project ID is required.")
			projects, errProjects := fetchGCPProjects(ctx, httpClient)
			if errProjects != nil {
				log.Fatalf("Failed to get project list: %v", errProjects)
				return
			}
			showProjectSelectionHelp(storage.Email, projects)
			return
		}
		log.Fatalf("Failed to complete user setup: %v", errSetup)
		return
	}

	storage.Auto = strings.TrimSpace(projectID) == ""

	if !storage.Auto && !storage.Checked {
		isChecked, errCheck := checkCloudAPIIsEnabled(ctx, httpClient, storage.ProjectID)
		if errCheck != nil {
			log.Fatalf("Failed to check if Cloud AI API is enabled: %v", errCheck)
			return
		}
		storage.Checked = isChecked
		if !isChecked {
			log.Fatal("Failed to check if Cloud AI API is enabled. If you encounter an error message, please create an issue.")
			return
		}
	}

	updateAuthRecord(record, storage)

	store := sdkAuth.GetTokenStore()
	if setter, okSetter := store.(interface{ SetBaseDir(string) }); okSetter && cfg != nil {
		setter.SetBaseDir(cfg.AuthDir)
	}

	savedPath, errSave := store.Save(ctx, record)
	if errSave != nil {
		log.Fatalf("Failed to save token to file: %v", errSave)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Gemini authentication successful!")
}

func performGeminiCLISetup(ctx context.Context, httpClient *http.Client, storage *gemini.GeminiTokenStorage, requestedProject string) error {
	metadata := map[string]string{
		"ideType":    "IDE_UNSPECIFIED",
		"platform":   "PLATFORM_UNSPECIFIED",
		"pluginType": "GEMINI",
	}

	loadReqBody := map[string]any{
		"metadata": metadata,
	}
	if requestedProject != "" {
		loadReqBody["cloudaicompanionProject"] = requestedProject
	}

	var loadResp map[string]any
	if errLoad := callGeminiCLI(ctx, httpClient, "loadCodeAssist", loadReqBody, &loadResp); errLoad != nil {
		return fmt.Errorf("load code assist: %w", errLoad)
	}

	tierID := "legacy-tier"
	if tiers, okTiers := loadResp["allowedTiers"].([]any); okTiers {
		for _, rawTier := range tiers {
			tier, okTier := rawTier.(map[string]any)
			if !okTier {
				continue
			}
			if isDefault, okDefault := tier["isDefault"].(bool); okDefault && isDefault {
				if id, okID := tier["id"].(string); okID && strings.TrimSpace(id) != "" {
					tierID = strings.TrimSpace(id)
					break
				}
			}
		}
	}

	projectID := strings.TrimSpace(requestedProject)
	if projectID == "" {
		if id, okProject := loadResp["cloudaicompanionProject"].(string); okProject {
			projectID = strings.TrimSpace(id)
		}
	}
	if projectID == "" {
		return &projectSelectionRequiredError{}
	}

	onboardReqBody := map[string]any{
		"tierId":                  tierID,
		"metadata":                metadata,
		"cloudaicompanionProject": projectID,
	}

	// Store the requested project as a fallback in case the response omits it.
	storage.ProjectID = projectID

	for {
		var onboardResp map[string]any
		if errOnboard := callGeminiCLI(ctx, httpClient, "onboardUser", onboardReqBody, &onboardResp); errOnboard != nil {
			return fmt.Errorf("onboard user: %w", errOnboard)
		}

		if done, okDone := onboardResp["done"].(bool); okDone && done {
			if resp, okResp := onboardResp["response"].(map[string]any); okResp {
				if project, okProject := resp["cloudaicompanionProject"].(map[string]any); okProject {
					if id, okID := project["id"].(string); okID && strings.TrimSpace(id) != "" {
						storage.ProjectID = strings.TrimSpace(id)
					}
				}
			}
			storage.ProjectID = strings.TrimSpace(storage.ProjectID)
			if storage.ProjectID == "" {
				storage.ProjectID = projectID
			}
			if storage.ProjectID == "" {
				return fmt.Errorf("onboard user completed without project id")
			}
			log.Infof("Onboarding complete. Using Project ID: %s", storage.ProjectID)
			return nil
		}

		log.Println("Onboarding in progress, waiting 5 seconds...")
		time.Sleep(5 * time.Second)
	}
}

func callGeminiCLI(ctx context.Context, httpClient *http.Client, endpoint string, body any, result any) error {
	url := fmt.Sprintf("%s/%s:%s", geminiCLIEndpoint, geminiCLIVersion, endpoint)
	if strings.HasPrefix(endpoint, "operations/") {
		url = fmt.Sprintf("%s/%s", geminiCLIEndpoint, endpoint)
	}

	var reader io.Reader
	if body != nil {
		rawBody, errMarshal := json.Marshal(body)
		if errMarshal != nil {
			return fmt.Errorf("marshal request body: %w", errMarshal)
		}
		reader = bytes.NewReader(rawBody)
	}

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, reader)
	if errRequest != nil {
		return fmt.Errorf("create request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", geminiCLIUserAgent)
	req.Header.Set("X-Goog-Api-Client", geminiCLIApiClient)
	req.Header.Set("Client-Metadata", geminiCLIClientMetadata)

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return fmt.Errorf("execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	if result == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if errDecode := json.NewDecoder(resp.Body).Decode(result); errDecode != nil {
		return fmt.Errorf("decode response body: %w", errDecode)
	}

	return nil
}

func fetchGCPProjects(ctx context.Context, httpClient *http.Client) ([]interfaces.GCPProjectProjects, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudresourcemanager.googleapis.com/v1/projects", nil)
	if errRequest != nil {
		return nil, fmt.Errorf("could not create project list request: %w", errRequest)
	}

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("failed to execute project list request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("project list request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var projects interfaces.GCPProject
	if errDecode := json.NewDecoder(resp.Body).Decode(&projects); errDecode != nil {
		return nil, fmt.Errorf("failed to unmarshal project list: %w", errDecode)
	}

	return projects.Projects, nil
}

func showProjectSelectionHelp(email string, projects []interfaces.GCPProjectProjects) {
	if email != "" {
		log.Infof("Your account %s needs to specify a project ID.", email)
	} else {
		log.Info("You need to specify a project ID.")
	}

	if len(projects) > 0 {
		fmt.Println("========================================================================")
		for _, p := range projects {
			fmt.Printf("Project ID: %s\n", p.ProjectID)
			fmt.Printf("Project Name: %s\n", p.Name)
			fmt.Println("------------------------------------------------------------------------")
		}
	} else {
		fmt.Println("No active projects were returned for this account.")
	}

	fmt.Printf("Please run this command to login again with a specific project:\n\n%s --login --project_id <project_id>\n", os.Args[0])
}

func checkCloudAPIIsEnabled(ctx context.Context, httpClient *http.Client, projectID string) (bool, error) {
	payload := fmt.Sprintf(`{"project":"%s","request":{"contents":[{"role":"user","parts":[{"text":"Be concise. What is the capital of France?"}]}],"generationConfig":{"thinkingConfig":{"include_thoughts":false,"thinkingBudget":0}}},"model":"gemini-2.5-flash"}`, projectID)

	url := fmt.Sprintf("%s/%s:%s?alt=sse", geminiCLIEndpoint, geminiCLIVersion, "streamGenerateContent")
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(payload))
	if errRequest != nil {
		return false, fmt.Errorf("failed to create request: %w", errRequest)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", geminiCLIUserAgent)
	req.Header.Set("X-Goog-Api-Client", geminiCLIApiClient)
	req.Header.Set("Client-Metadata", geminiCLIClientMetadata)
	req.Header.Set("Accept", "text/event-stream")

	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		return false, fmt.Errorf("failed to execute request: %w", errDo)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		bodyBytes, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusForbidden {
			activationURL := gjson.GetBytes(bodyBytes, "0.error.details.0.metadata.activationUrl").String()
			if activationURL != "" {
				log.Warnf("\n\nPlease activate your account with this url:\n\n%s\n\n And execute this command again:\n%s --login --project_id %s", activationURL, os.Args[0], projectID)
				return false, nil
			}
			log.Warnf("\n\nPlease copy this message and create an issue.\n\n%s\n\n", strings.TrimSpace(string(bodyBytes)))
			return false, nil
		}
		return false, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		// Consume the stream to ensure the request succeeds.
	}
	if errScan := scanner.Err(); errScan != nil {
		return false, fmt.Errorf("stream read failed: %w", errScan)
	}

	return true, nil
}

func updateAuthRecord(record *cliproxyauth.Auth, storage *gemini.GeminiTokenStorage) {
	if record == nil || storage == nil {
		return
	}

	finalName := fmt.Sprintf("%s-%s.json", storage.Email, storage.ProjectID)

	if record.Metadata == nil {
		record.Metadata = make(map[string]any)
	}
	record.Metadata["email"] = storage.Email
	record.Metadata["project_id"] = storage.ProjectID
	record.Metadata["auto"] = storage.Auto
	record.Metadata["checked"] = storage.Checked

	record.ID = finalName
	record.FileName = finalName
	record.Storage = storage
}
