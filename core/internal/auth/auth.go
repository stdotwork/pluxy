package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"golang.org/x/net/proxy"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/skratchdot/open-golang/open"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	oauthClientID     = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"
	oauthClientSecret = "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"
)

var (
	oauthScopes = []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
	}
)

type TokenStorage struct {
	Token     any    `json:"token"`
	ProjectID string `json:"project_id"`
	Email     string `json:"email"`
}

// GetAuthenticatedClient configures and returns an HTTP client with OAuth2 tokens.
// It handles the entire flow: loading, refreshing, and fetching new tokens.
func GetAuthenticatedClient(ctx context.Context, ts *TokenStorage, cfg *config.Config) (*http.Client, error) {
	proxyURL, err := url.Parse(cfg.ProxyUrl)
	if err == nil {
		if proxyURL.Scheme == "socks5" {
			username := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			auth := &proxy.Auth{
				User:     username,
				Password: password,
			}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, auth, proxy.Direct)
			if errSOCKS5 != nil {
				log.Fatalf("create SOCKS5 dialer failed: %v", errSOCKS5)
			}

			transport := &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (c net.Conn, err error) {
					return dialer.Dial(network, addr)
				},
			}
			proxyClient := &http.Client{
				Transport: transport,
			}

			ctx = context.WithValue(ctx, oauth2.HTTPClient, proxyClient)
		} else if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
			transport := &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
			proxyClient := &http.Client{
				Transport: transport,
			}
			ctx = context.WithValue(ctx, oauth2.HTTPClient, proxyClient)
		}
	}

	conf := &oauth2.Config{
		ClientID:     oauthClientID,
		ClientSecret: oauthClientSecret,
		RedirectURL:  "http://localhost:8085/oauth2callback", // Placeholder, will be updated
		Scopes:       oauthScopes,
		Endpoint:     google.Endpoint,
	}

	var token *oauth2.Token

	if ts.Token == nil {
		log.Info("Could not load token from file, starting OAuth flow.")
		token, err = getTokenFromWeb(ctx, conf)
		if err != nil {
			return nil, fmt.Errorf("failed to get token from web: %w", err)
		}
		ts, err = saveTokenToFile(ctx, conf, token, ts.ProjectID, cfg.AuthDir)
		if err != nil {
			// Log the error but proceed, as we have a valid token for the session.
			log.Errorf("Warning: failed to save token to file: %v", err)
		}
	}
	tsToken, _ := json.Marshal(ts.Token)
	if err = json.Unmarshal(tsToken, &token); err != nil {
		return nil, err
	}

	return conf.Client(ctx, token), nil
}

// saveTokenToFile saves a token to the local credentials file.
func saveTokenToFile(ctx context.Context, config *oauth2.Config, token *oauth2.Token, projectID, authDir string) (*TokenStorage, error) {
	httpClient := config.Client(ctx, token)
	req, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return nil, fmt.Errorf("could not get user info: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))

	resp, err := httpClient.Do(req)
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

	emailResult := gjson.GetBytes(bodyBytes, "email")
	if emailResult.Exists() && emailResult.Type == gjson.String {
		log.Infof("Authenticated user email: %s", emailResult.String())
	} else {
		log.Info("Failed to get user email from token")
	}

	log.Infof("Saving credentials to %s", filepath.Join(authDir, fmt.Sprintf("%s.json", emailResult.String())))
	if err = os.MkdirAll(authDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create directory: %w", err)
	}

	f, err := os.Create(filepath.Join(authDir, fmt.Sprintf("%s.json", emailResult.String())))
	if err != nil {
		return nil, fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	var ifToken map[string]any
	jsonData, _ := json.Marshal(token)
	err = json.Unmarshal(jsonData, &ifToken)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal token: %w", err)
	}

	ifToken["token_uri"] = "https://oauth2.googleapis.com/token"
	ifToken["client_id"] = oauthClientID
	ifToken["client_secret"] = oauthClientSecret
	ifToken["scopes"] = oauthScopes
	ifToken["universe_domain"] = "googleapis.com"

	ts := TokenStorage{
		Token:     ifToken,
		ProjectID: projectID,
		Email:     emailResult.String(),
	}

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return nil, fmt.Errorf("failed to write token to file: %w", err)
	}
	return &ts, nil
}

// getTokenFromWeb starts a local server to handle the OAuth2 flow.
func getTokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	// Use a channel to pass the authorization code from the HTTP handler to the main function.
	codeChan := make(chan string)
	errChan := make(chan error)

	// Create a new HTTP server.
	server := &http.Server{Addr: "localhost:8085"}
	config.RedirectURL = "http://localhost:8085/oauth2callback"

	http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		if err := r.URL.Query().Get("error"); err != "" {
			_, _ = fmt.Fprintf(w, "Authentication failed: %s", err)
			errChan <- fmt.Errorf("authentication failed via callback: %s", err)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			_, _ = fmt.Fprint(w, "Authentication failed: code not found.")
			errChan <- fmt.Errorf("code not found in callback")
			return
		}
		_, _ = fmt.Fprint(w, "<html><body><h1>Authentication successful!</h1><p>You can close this window.</p></body></html>")
		codeChan <- code
	})

	// Start the server in a goroutine.
	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("ListenAndServe(): %v", err)
		}
	}()

	// Open the authorization URL in the user's browser.
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	log.Debugf("CLI login required.\nAttempting to open authentication page in your browser.\nIf it does not open, please navigate to this URL:\n\n%s\n\n", authURL)

	err := open.Run(authURL)
	if err != nil {
		log.Errorf("Failed to open browser: %v. Please open the URL manually.", err)
	}

	// Wait for the authorization code or an error.
	var authCode string
	select {
	case code := <-codeChan:
		authCode = code
	case err = <-errChan:
		return nil, err
	case <-time.After(5 * time.Minute): // Timeout
		return nil, fmt.Errorf("oauth flow timed out")
	}

	// Shutdown the server.
	if err = server.Shutdown(ctx); err != nil {
		log.Errorf("Failed to shut down server: %v", err)
	}

	// Exchange the authorization code for a token.
	token, err := config.Exchange(ctx, authCode)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}

	log.Info("Authentication successful.")
	return token, nil
}
