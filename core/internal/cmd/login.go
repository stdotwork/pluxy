// Package cmd provides command-line interface functionality for the CLI Proxy API server.
// It includes authentication flows for various AI service providers, service startup,
// and other command-line operations.
package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoLogin handles Google Gemini authentication using the shared authentication manager.
// It initiates the OAuth flow for Google Gemini services and saves the authentication
// tokens to the configured auth directory.
//
// Parameters:
//   - cfg: The application configuration
//   - projectID: Optional Google Cloud project ID for Gemini services
//   - options: Login options including browser behavior and prompts
func DoLogin(cfg *config.Config, projectID string, options *LoginOptions) {
	if options == nil {
		options = &LoginOptions{}
	}

	manager := newAuthManager()

	metadata := map[string]string{}
	if projectID != "" {
		metadata["project_id"] = projectID
	}

	authOpts := &sdkAuth.LoginOptions{
		NoBrowser: options.NoBrowser,
		ProjectID: projectID,
		Metadata:  metadata,
		Prompt:    options.Prompt,
	}

	_, savedPath, err := manager.Login(context.Background(), "gemini", cfg, authOpts)
	if err != nil {
		var selectionErr *sdkAuth.ProjectSelectionError
		if errors.As(err, &selectionErr) {
			fmt.Println(selectionErr.Error())
			projects := selectionErr.ProjectsDisplay()
			if len(projects) > 0 {
				fmt.Println("========================================================================")
				for _, p := range projects {
					fmt.Printf("Project ID: %s\n", p.ProjectID)
					fmt.Printf("Project Name: %s\n", p.Name)
					fmt.Println("------------------------------------------------------------------------")
				}
				fmt.Println("Please rerun the login command with --project_id <project_id>.")
			}
			return
		}
		log.Fatalf("Gemini authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		fmt.Printf("Authentication saved to %s\n", savedPath)
	}

	fmt.Println("Gemini authentication successful!")
}
