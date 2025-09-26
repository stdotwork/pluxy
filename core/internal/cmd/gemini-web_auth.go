// Package cmd provides command-line interface functionality for the CLI Proxy API.
package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/auth/gemini"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v6/sdk/auth"
)

// banner prints a simple ASCII banner for clarity without ANSI colors.
func banner(title string) {
	line := strings.Repeat("=", len(title)+8)
	fmt.Println(line)
	fmt.Println("=== " + title + " ===")
	fmt.Println(line)
}

// DoGeminiWebAuth handles the process of creating a Gemini Web token file.
// New flow:
//  1. Prompt user to paste the full cookie string.
//  2. Extract __Secure-1PSID and __Secure-1PSIDTS from the cookie string.
//  3. Call https://accounts.google.com/ListAccounts with the cookie to obtain email.
//  4. Save auth file with the same structure, and set Label to the email.
func DoGeminiWebAuth(cfg *config.Config) {
	var secure1psid, secure1psidts, email string

	reader := bufio.NewReader(os.Stdin)
	isMacOS := strings.HasPrefix(runtime.GOOS, "darwin")
	cookieProvided := false
	banner("Gemini Web Cookie Sign-in")
	if !isMacOS {
		// NOTE: Provide extra guidance for macOS users or anyone unsure about retrieving cookies.
		fmt.Println("--- Cookie Input ---")
		fmt.Println(">> Paste your full Google Cookie and press Enter")
		fmt.Println("Tip: If you are on macOS, or don't know how to get the cookie, just press Enter and follow the prompts.")
		fmt.Print("Cookie: ")
		rawCookie, _ := reader.ReadString('\n')
		rawCookie = strings.TrimSpace(rawCookie)
		if rawCookie == "" {
			// Skip cookie-based parsing; fall back to manual field prompts.
			fmt.Println("==> No cookie provided. Proceeding with manual input.")
		} else {
			cookieProvided = true
			// Parse K=V cookie pairs separated by ';'
			cookieMap := make(map[string]string)
			parts := strings.Split(rawCookie, ";")
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p == "" {
					continue
				}
				if eq := strings.Index(p, "="); eq > 0 {
					k := strings.TrimSpace(p[:eq])
					v := strings.TrimSpace(p[eq+1:])
					if k != "" {
						cookieMap[k] = v
					}
				}
			}
			secure1psid = strings.TrimSpace(cookieMap["__Secure-1PSID"])
			secure1psidts = strings.TrimSpace(cookieMap["__Secure-1PSIDTS"])

			// Build HTTP client with proxy settings respected.
			httpClient := &http.Client{Timeout: 15 * time.Second}
			httpClient = util.SetProxy(cfg, httpClient)

			// Request ListAccounts to extract email as label (use POST per upstream behavior).
			req, err := http.NewRequest(http.MethodPost, "https://accounts.google.com/ListAccounts", nil)
			if err != nil {
				fmt.Println("!! Failed to create request:", err)
			} else {
				req.Header.Set("Cookie", rawCookie)
				req.Header.Set("Accept", "application/json, text/plain, */*")
				req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
				req.Header.Set("Origin", "https://accounts.google.com")
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")

				resp, err := httpClient.Do(req)
				if err != nil {
					fmt.Println("!! Request to ListAccounts failed:", err)
				} else {
					defer func() { _ = resp.Body.Close() }()
					if resp.StatusCode != http.StatusOK {
						fmt.Printf("!! ListAccounts returned status code: %d\n", resp.StatusCode)
					} else {
						var payload []any
						if err = json.NewDecoder(resp.Body).Decode(&payload); err != nil {
							fmt.Println("!! Failed to parse ListAccounts response:", err)
						} else {
							// Expected structure like: ["gaia.l.a.r", [["gaia.l.a",1,"Name","email@example.com", ... ]]]
							if len(payload) >= 2 {
								if accounts, ok := payload[1].([]any); ok && len(accounts) >= 1 {
									if first, ok1 := accounts[0].([]any); ok1 && len(first) >= 4 {
										if em, ok2 := first[3].(string); ok2 {
											email = strings.TrimSpace(em)
										}
									}
								}
							}
							if email == "" {
								fmt.Println("!! Failed to parse email from ListAccounts response")
							}
						}
					}
				}
			}
		}
	}

	// Fallback: prompt user to input missing values
	if secure1psid == "" {
		if cookieProvided && !isMacOS {
			fmt.Println("!! Cookie missing __Secure-1PSID.")
		}
		fmt.Print("Enter __Secure-1PSID: ")
		v, _ := reader.ReadString('\n')
		secure1psid = strings.TrimSpace(v)
	}
	if secure1psidts == "" {
		if cookieProvided && !isMacOS {
			fmt.Println("!! Cookie missing __Secure-1PSIDTS.")
		}
		fmt.Print("Enter __Secure-1PSIDTS: ")
		v, _ := reader.ReadString('\n')
		secure1psidts = strings.TrimSpace(v)
	}
	if secure1psid == "" || secure1psidts == "" {
		// Use print instead of logger to avoid log redirection.
		fmt.Println("!! __Secure-1PSID and __Secure-1PSIDTS cannot be empty")
		return
	}
	if isMacOS {
		fmt.Print("Enter your account email: ")
		v, _ := reader.ReadString('\n')
		email = strings.TrimSpace(v)
	}

	// Generate a filename based on the SHA256 hash of the PSID
	hasher := sha256.New()
	hasher.Write([]byte(secure1psid))
	hash := hex.EncodeToString(hasher.Sum(nil))
	fileName := fmt.Sprintf("gemini-web-%s.json", hash[:16])

	// Decide label: prefer email; fallback prompt then file name without .json
	defaultLabel := strings.TrimSuffix(fileName, ".json")
	label := email
	if label == "" {
		fmt.Print(fmt.Sprintf("Enter label for this auth (default: %s): ", defaultLabel))
		v, _ := reader.ReadString('\n')
		v = strings.TrimSpace(v)
		if v != "" {
			label = v
		} else {
			label = defaultLabel
		}
	}

	tokenStorage := &gemini.GeminiWebTokenStorage{
		Secure1PSID:   secure1psid,
		Secure1PSIDTS: secure1psidts,
		Label:         label,
	}
	record := &sdkAuth.TokenRecord{
		Provider: "gemini-web",
		FileName: fileName,
		Storage:  tokenStorage,
	}
	store := sdkAuth.GetTokenStore()
	savedPath, err := store.Save(context.Background(), cfg, record)
	if err != nil {
		fmt.Println("!! Failed to save Gemini Web token to file:", err)
		return
	}

	fmt.Println("==> Successfully saved Gemini Web token!")
	fmt.Println("==> Saved to:", savedPath)
}
