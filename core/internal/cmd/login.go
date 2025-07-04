package cmd

import (
	"context"
	"github.com/luispater/CLIProxyAPI/internal/auth"
	"github.com/luispater/CLIProxyAPI/internal/client"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"os"
)

func DoLogin(cfg *config.Config, projectID string) {
	var err error
	var ts auth.TokenStorage
	if projectID != "" {
		ts.ProjectID = projectID
	}

	// 2. Initialize authenticated HTTP Client
	clientCtx := context.Background()

	log.Info("Initializing authentication...")
	httpClient, errGetClient := auth.GetAuthenticatedClient(clientCtx, &ts, cfg)
	if errGetClient != nil {
		log.Fatalf("failed to get authenticated client: %v", errGetClient)
		return
	}
	log.Info("Authentication successful.")

	// 3. Initialize CLI Client
	cliClient := client.NewClient(httpClient, &ts, cfg)
	err = cliClient.SetupUser(clientCtx, ts.Email, projectID)
	if err != nil {
		if err.Error() == "failed to start user onboarding, need define a project id" {
			log.Error("failed to start user onboarding")
			project, errGetProjectList := cliClient.GetProjectList(clientCtx)
			if errGetProjectList != nil {
				log.Fatalf("failed to complete user setup: %v", err)
			} else {
				log.Infof("Your account %s needs specify a project id.", ts.Email)
				log.Info("========================================================================")
				for i := 0; i < len(project.Projects); i++ {
					log.Infof("Project ID: %s", project.Projects[i].ProjectID)
					log.Infof("Project Name: %s", project.Projects[i].Name)
					log.Info("========================================================================")
				}
				log.Infof("Please run this command to login again:\n\n%s --login --project_id <project_id>\n", os.Args[0])
			}
		} else {
			// Log as a warning because in some cases, the CLI might still be usable
			// or the user might want to retry setup later.
			log.Fatalf("failed to complete user setup: %v", err)
		}
	} else {
		auto := projectID == ""
		cliClient.SetIsAuto(auto)

		if !cliClient.IsChecked() && !cliClient.IsAuto() {
			isChecked, checkErr := cliClient.CheckCloudAPIIsEnabled()
			if checkErr != nil {
				log.Fatalf("failed to check cloud api is enabled: %v", checkErr)
				return
			}
			cliClient.SetIsChecked(isChecked)
		}

		if !cliClient.IsChecked() && !cliClient.IsAuto() {
			return
		}

		err = cliClient.SaveTokenToFile()
		if err != nil {
			log.Fatal(err)
			return
		}
	}
}
