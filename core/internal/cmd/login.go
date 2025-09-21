package cmd

import (
	"context"
	"errors"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
	log "github.com/sirupsen/logrus"
)

// DoLogin handles Google Gemini authentication using the shared authentication manager.
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
			log.Error(selectionErr.Error())
			projects := selectionErr.ProjectsDisplay()
			if len(projects) > 0 {
				log.Info("========================================================================")
				for _, p := range projects {
					log.Infof("Project ID: %s", p.ProjectID)
					log.Infof("Project Name: %s", p.Name)
					log.Info("------------------------------------------------------------------------")
				}
				log.Info("Please rerun the login command with --project_id <project_id>.")
			}
			return
		}
		log.Fatalf("Gemini authentication failed: %v", err)
		return
	}

	if savedPath != "" {
		log.Infof("Authentication saved to %s", savedPath)
	}

	log.Info("Gemini authentication successful!")
}
