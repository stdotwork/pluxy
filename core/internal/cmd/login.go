// Package cmd provides command-line interface functionality for the CLI Proxy API.
// It implements the main application commands including login/authentication
// and server startup, handling the complete user onboarding and service lifecycle.
package cmd

import (
	"context"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"os"
)

// DoLogin handles the entire user login and setup process.
// It authenticates the user, sets up the user's project, checks API enablement,
// and saves the token for future use.
func DoLogin(cfg *config.Config, projectID string) {
	var err error
	var ts auth.TokenStorage
	if projectID != "" {
		ts.ProjectID = projectID
	}

	// Initialize an authenticated HTTP client. This will trigger the OAuth flow if necessary.
	clientCtx := context.Background()
	log.Info("Initializing authentication...")
	httpClient, errGetClient := auth.GetAuthenticatedClient(clientCtx, &ts, cfg)
	if errGetClient != nil {
		log.Fatalf("failed to get authenticated client: %v", errGetClient)
		return
	}
	log.Info("Authentication successful.")

	// Initialize the API client.
	cliClient := client.NewClient(httpClient, &ts, cfg)

	// Perform the user setup process.
	err = cliClient.SetupUser(clientCtx, ts.Email, projectID)
	if err != nil {
		// Handle the specific case where a project ID is required but not provided.
		if err.Error() == "failed to start user onboarding, need define a project id" {
			log.Error("Failed to start user onboarding: A project ID is required.")
			// Fetch and display the user's available projects to help them choose one.
			project, errGetProjectList := cliClient.GetProjectList(clientCtx)
			if errGetProjectList != nil {
				log.Fatalf("Failed to get project list: %v", err)
			} else {
				log.Infof("Your account %s needs to specify a project ID.", ts.Email)
				log.Info("========================================================================")
				for _, p := range project.Projects {
					log.Infof("Project ID: %s", p.ProjectID)
					log.Infof("Project Name: %s", p.Name)
					log.Info("------------------------------------------------------------------------")
				}
				log.Infof("Please run this command to login again with a specific project:\n\n%s --login --project_id <project_id>\n", os.Args[0])
			}
		} else {
			log.Fatalf("Failed to complete user setup: %v", err)
		}
		return // Exit after handling the error.
	}

	// If setup is successful, proceed to check API status and save the token.
	auto := projectID == ""
	cliClient.SetIsAuto(auto)

	// If the project was not automatically selected, check if the Cloud AI API is enabled.
	if !cliClient.IsChecked() && !cliClient.IsAuto() {
		isChecked, checkErr := cliClient.CheckCloudAPIIsEnabled()
		if checkErr != nil {
			log.Fatalf("Failed to check if Cloud AI API is enabled: %v", checkErr)
			return
		}
		cliClient.SetIsChecked(isChecked)
		// If the check fails (returns false), the CheckCloudAPIIsEnabled function
		// will have already printed instructions, so we can just exit.
		if !isChecked {
			log.Fatal("Failed to check if Cloud AI API is enabled. If you encounter an error message, please create an issue.")
			return
		}
	}

	// Save the successfully obtained and verified token to a file.
	err = cliClient.SaveTokenToFile()
	if err != nil {
		log.Fatalf("Failed to save token to file: %v", err)
	}
}
