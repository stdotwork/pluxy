package cmd

import (
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

func newAuthManager() *sdkAuth.Manager {
	store := sdkAuth.NewFileTokenStore()
	manager := sdkAuth.NewManager(store,
		sdkAuth.NewGeminiAuthenticator(),
		sdkAuth.NewCodexAuthenticator(),
		sdkAuth.NewClaudeAuthenticator(),
		sdkAuth.NewQwenAuthenticator(),
	)
	return manager
}
