package corpus

import (
	"bufio"
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Core is a running core under test.
type Core struct {
	// URL is the base URL clients use, for example http://127.0.0.1:43121.
	URL  string
	stop func()
	logs *logBuffer
}

// Stop ends the core and waits for it to exit.
func (c *Core) Stop() { c.stop() }

// Logs returns what the core wrote to stdout and stderr.
func (c *Core) Logs() string { return c.logs.String() }

// Provider tells the runner how to point each core at the fake upstream for one provider
// directory under cases/<format>/.
type Provider struct {
	// OldCore returns the old core's api-keys block for the provider, given the fake upstream
	// URL, the models the cases use and one upstream key per account.
	OldCore func(upstreamURL string, models, keys []string) map[string]any
	// NewCore returns the new core's extension name (a key of NewCoreExtensions) and its
	// accounts for the provider, one per key. models is empty when the accounts serve any
	// model.
	NewCore func(upstreamURL string, models, keys []string) (extension string, accounts []any)
	// OldCoreAuthFiles, when set, returns auth files to write into the old core's auth folder,
	// by file name, one account per key. proxyURL is the intercepting proxy the files must name
	// when Intercept is set; upstreamURL is the fake upstream, for a file that names its base URL.
	OldCoreAuthFiles func(proxyURL, upstreamURL string, models, keys []string) map[string]any
	// Intercept runs the old core through the intercepting proxy (see InterceptHosts), for a
	// provider whose upstream host no setting moves.
	Intercept bool
	// Aside, when set, answers the requests a core makes on its own, outside the case (such as
	// a model list it fetches when it loads an account), so they are not recorded and use up no
	// scripted reply.
	Aside func(r *http.Request) (Reply, bool)
	// Proto, when set, decodes the provider's protobuf requests over Connect into JSON for
	// comparison (see connect.go).
	Proto *ProtoSchema
}

// Providers maps a provider directory name to its setup. Later slices add theirs here.
var Providers = map[string]Provider{
	"openai-compatible": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"openai-compatibility": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "openai-compatible", accounts
		},
	},
	// An upstream that speaks OpenAI Responses with an API key. The new core uses the
	// OpenAI-compatible extension with wire_api "responses". Today's core has no generic
	// Responses upstream; the closest is a Codex API key with a custom base URL, with the
	// Codex client identity (cloaking) turned off.
	"openai-responses": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k, "disable-codex-cloaking": true})
			}
			return map[string]any{"codex": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1", "wire_api": "responses"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "openai-compatible", accounts
		},
	},
	// A Codex API key: the Codex provider extension in the new core, a codex-api-key entry with
	// the Codex client identity on (the default) in today's core. Both send requests to
	// {base_url}/responses.
	"codex": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"codex": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "codex", accounts
		},
	},
	// A Codex API key with the upstream WebSocket on: in the new core the Codex provider
	// extension with "websockets": true in the account's settings, in today's core a
	// codex-api-key entry whose key has "websockets": true. Turns of a WebSocket client go to
	// {base_url}/responses over a WebSocket; other requests over HTTP as for "codex".
	"codex-ws": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k, "websockets": true})
			}
			return map[string]any{"codex": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1", "websockets": true},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "codex", accounts
		},
	},
	// A ChatGPT subscription signed in to Codex: the Codex provider extension with an OAuth
	// credential in the new core, a Codex auth file in today's core. Today's core always calls
	// https://chatgpt.com/backend-api/codex, so it runs through the intercepting proxy; the new
	// core's account points base_url at the same path on the fake upstream. The tokens are fake
	// JWTs that expire in 2100, so neither core tries to renew them.
	"codex-oauth": {
		Intercept: true,
		OldCore:   func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(proxyURL, _ string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i, k := range keys {
				t := corpusTokens(k)
				f := map[string]any{
					"type":          "codex",
					"email":         t.email,
					"account_id":    t.accountID,
					"plan_type":     "plus",
					"access_token":  t.access,
					"id_token":      t.id,
					"refresh_token": t.refresh,
					"expired":       "2100-01-01T00:00:00Z",
					"last_refresh":  "2026-01-01T00:00:00Z",
					"proxy_url":     proxyURL,
				}
				files[fmt.Sprintf("codex-corpus-%d.json", i+1)] = f
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				t := corpusTokens(k)
				acct := map[string]any{
					"id":       id,
					"settings": map[string]any{"base_url": upstreamURL + "/backend-api/codex"},
					"credential": map[string]any{"kind": "oauth", "access_token": t.access, "refresh_token": t.refresh,
						"expires_at": corpusTokenExpiry, "data": map[string]any{"id_token": t.id, "account_id": t.accountID}},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "codex", accounts
		},
	},
	// An upstream that speaks Anthropic Messages with an API key. The new core uses the
	// OpenAI-compatible extension with wire_api "messages"; today's core uses a Claude API key
	// with a custom base URL.
	"anthropic": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"claude": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL,
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1", "wire_api": "messages"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "openai-compatible", accounts
		},
	},
	// Anthropic's Claude with an API key: the new core's Claude extension against today's Claude
	// API-key provider, both with a custom base URL. Today's core cannot point a Claude
	// subscription login at a fake upstream (its OAuth accounts always call api.anthropic.com),
	// so subscription behaviour is checked by the e2e tests instead.
	"claude": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"claude": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL,
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "claude", accounts
		},
	},
	// An upstream that speaks Gemini GenerateContent with an API key. The new core uses the
	// OpenAI-compatible extension with wire_api "gemini" (the Gemini provider extension comes
	// later); today's core uses a Gemini API key with a custom base URL. Both send requests to
	// {upstream}/v1beta/models/{model}:{method}.
	"gemini": {
		OldCore: oldCoreGeminiAPIKey,
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1beta", "wire_api": "gemini"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "openai-compatible", accounts
		},
	},
	// Gemini with an API key: the new core's Gemini extension against today's Gemini API-key
	// provider, both with a custom base URL. Both send requests to
	// {upstream}/v1beta/models/{model}:{method} with the key in x-goog-api-key.
	"gemini-api-key": {
		OldCore: oldCoreGeminiAPIKey,
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "gemini", accounts
		},
	},
	// Vertex AI with an express mode API key: the Gemini extension with vertex settings against
	// today's vertex-api-key entry, both with a custom base URL. Both send requests to
	// {upstream}/v1/publishers/google/models/{model}:{method} with the key in x-goog-api-key.
	"vertex-api-key": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"vertex": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL,
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL, "vertex": map[string]any{}},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "gemini", accounts
		},
	},
	// Vertex AI with a service account: the Gemini extension against a Vertex auth file in
	// today's core. Today's core always calls https://aiplatform.googleapis.com (location global)
	// and trades the service account's JWT at https://oauth2.googleapis.com/token, so it runs
	// through the intercepting proxy; the new core's account points base_url and the key's
	// token_uri at the same paths on the fake upstream, and holds only the key's secret part
	// (the project is in its settings). The upstream's first reply in each case
	// is therefore the token endpoint's. The key is an RSA key made for the run.
	"vertex": {
		Intercept: true,
		OldCore:   func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(proxyURL, _ string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i := range keys {
				files[fmt.Sprintf("vertex-corpus-%d.json", i+1)] = map[string]any{
					"type":            "vertex",
					"project_id":      corpusVertexProject,
					"email":           corpusServiceAccountEmail(i),
					"location":        "global",
					"service_account": corpusServiceAccount(i, ""),
					"proxy_url":       proxyURL,
				}
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":       id,
					"settings": map[string]any{"base_url": upstreamURL, "vertex": map[string]any{"project": corpusVertexProject}},
					"credential": map[string]any{"kind": "service_account", "data": map[string]any{
						"client_email": corpusServiceAccountEmail(i), "private_key": corpusRSA(), "token_uri": upstreamURL + "/token"}},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "gemini", accounts
		},
	},
	// A Kimi Code subscription: the Kimi extension with an OAuth credential against a Kimi auth
	// file in today's core, both with base_url pointed at the fake upstream (the default is
	// https://api.kimi.com/coding). The tokens are fake and expire in 2100, so neither core
	// renews them; today's core serves a subscription only the models in its catalog.
	"kimi": {
		OldCore: func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(_, upstreamURL string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i, k := range keys {
				files[fmt.Sprintf("kimi-corpus-%d.json", i+1)] = map[string]any{
					"type":          "kimi",
					"access_token":  k,
					"refresh_token": k + "-refresh",
					"token_type":    "Bearer",
					"timestamp":     1767225600000,
					"domain":        "kimi.com",
					"base_url":      upstreamURL + "/coding",
					"expired":       "2100-01-01T00:00:00Z",
					"device_id":     corpusKimiDeviceID,
				}
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":       id,
					"settings": map[string]any{"base_url": upstreamURL + "/coding"},
					"credential": map[string]any{"kind": "oauth", "access_token": k, "refresh_token": k + "-refresh",
						"expires_at": corpusTokenExpiry, "data": map[string]any{"device_id": corpusKimiDeviceID}},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "kimi", accounts
		},
	},
	// An Antigravity login: the Antigravity extension with an OAuth credential against an
	// Antigravity auth file in today's core, both with base_url pointed at the fake upstream (the
	// default is https://daily-cloudcode-pa.googleapis.com) and the same Google Cloud project.
	// The tokens are fake and expire in 2100, so neither core renews them or looks up the
	// project; today's core serves an Antigravity login only the models in its catalog.
	"antigravity": {
		OldCore: func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(_, upstreamURL string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i, k := range keys {
				files[fmt.Sprintf("antigravity-corpus-%d.json", i+1)] = map[string]any{
					"type":          "antigravity",
					"access_token":  k,
					"refresh_token": k + "-refresh",
					"expires_in":    3599,
					"timestamp":     1767225600000,
					"expired":       "2100-01-01T00:00:00Z",
					"email":         fmt.Sprintf("corpus-%d@example.com", i+1),
					"project_id":    corpusAntigravityProject,
					"base_url":      upstreamURL,
					// Today's core serves an Antigravity login only the models its
					// fetchAvailableModels probe found; a saved probe result stands in for it.
					"antigravity_capability_hints": map[string]any{
						"project_id": corpusAntigravityProject, "endpoint": upstreamURL, "model_ids": models},
				}
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":       id,
					"settings": map[string]any{"base_url": upstreamURL},
					"credential": map[string]any{"kind": "oauth", "access_token": k, "refresh_token": k + "-refresh",
						"expires_at": corpusTokenExpiry, "data": map[string]any{"project_id": corpusAntigravityProject, "client_secret": "corpus-client-secret"}},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "antigravity", accounts
		},
		// Today's core asks Cloud Code which models each Antigravity login has
		// (fetchAvailableModels) when it loads the account and again in the background; the
		// answer lists the models the cases use.
		Aside: func(r *http.Request) (Reply, bool) {
			if r.URL.Path != "/v1internal:fetchAvailableModels" {
				return Reply{}, false
			}
			return Reply{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"},
				Body: json.RawMessage(`{"models":{"gemini-3.8-flash-high":{},"claude-sonnet-5-5-high":{}}}`)}, true
		},
	},
	// A Muse Code login: the Meta extension with an OAuth credential (the minted API key as its
	// access token, the device token as its refresh token) against a Meta auth file in today's
	// core, both with base_url pointed at the fake upstream (the default is
	// https://api.meta.ai/v1). The key is already minted, so neither core calls Meta's key
	// endpoint; today's core serves a login only the models in its catalog.
	"meta": {
		OldCore: func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(_, upstreamURL string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i, k := range keys {
				files[fmt.Sprintf("meta-corpus-%d.json", i+1)] = map[string]any{
					"type":         "meta",
					"auth_kind":    "oauth",
					"access_token": k,
					"api_key":      k,
					"dca_token":    "dca:" + k,
					"token_type":   "Bearer",
					"base_url":     upstreamURL + "/v1",
					"email":        fmt.Sprintf("corpus-%d@example.com", i+1),
				}
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "oauth", "access_token": k, "refresh_token": "dca:" + k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "meta", accounts
		},
	},
	// A Devin login: the Devin extension with a session token, a devin auth file in today's
	// core; both send Devin's protobuf chat request over Connect to
	// {base_url}/exa.api_server_pb.ApiServerService/GetChatMessage.
	"devin": {
		OldCore: func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(_, upstreamURL string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i, k := range keys {
				files[fmt.Sprintf("devin-corpus-%d.json", i+1)] = map[string]any{
					"type":          "devin",
					"auth_kind":     "oauth",
					"api_key":       k,
					"session_token": k,
					"user_name":     fmt.Sprintf("corpus-%d", i+1),
					"base_url":      upstreamURL,
				}
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "devin", accounts
		},
		// Today's core asks for the account's plan and quota on its own (GetUserStatus).
		Aside: func(r *http.Request) (Reply, bool) {
			if strings.HasSuffix(r.URL.Path, "/GetUserStatus") {
				empty := ""
				return Reply{Headers: map[string]string{"Content-Type": "application/proto"}, BodyText: &empty}, true
			}
			return Reply{}, false
		},
		Proto: &ProtoSchema{
			Messages: []string{"1", "3", "3.6", "3.10", "8", "10", "15"},
			Repeated: []string{"3", "3.6", "3.10", "10"},
		},
	},
	// A Meta API key: the Meta extension with an api_key credential, a meta group in today's
	// core's api-keys, both sending Responses requests to {base_url}/responses.
	"meta-api-key": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m, "alias": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"meta": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "meta", accounts
		},
	},
	// An xAI API key: the xAI provider extension in the new core, an xai group in today's core.
	// Both send Responses requests to {base_url}/responses with the key as a bearer token.
	// A Grok login: the xAI extension with an OAuth credential, an xai auth file with auth_kind
	// oauth in today's core. Both point base_url at the fake upstream, so neither sends to the
	// Grok CLI's endpoint; today's core then sends the standard headers, the extension the CLI's.
	"xai-cli": {
		OldCore: func(string, []string, []string) map[string]any { return map[string]any{} },
		OldCoreAuthFiles: func(_, upstreamURL string, models, keys []string) map[string]any {
			files := map[string]any{}
			for i, k := range keys {
				files[fmt.Sprintf("xai-corpus-%d.json", i+1)] = map[string]any{
					"type":          "xai",
					"auth_kind":     "oauth",
					"access_token":  k,
					"refresh_token": "refresh-" + k,
					"expired":       "2100-01-01T00:00:00Z",
					"email":         fmt.Sprintf("corpus-%d@example.com", i+1),
					"base_url":      upstreamURL + "/v1",
				}
			}
			return files
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "oauth", "access_token": k, "refresh_token": "refresh-" + k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "xai", accounts
		},
	},
	"xai": {
		OldCore: func(upstreamURL string, models, keys []string) map[string]any {
			var ms []any
			for _, m := range models {
				ms = append(ms, map[string]any{"name": m})
			}
			var ks []any
			for _, k := range keys {
				ks = append(ks, map[string]any{"api-key": k})
			}
			return map[string]any{"xai": []any{map[string]any{
				"name":     "corpus",
				"base-url": upstreamURL + "/v1",
				"keys":     ks,
				"models":   ms,
			}}}
		},
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "api_key", "api_key": k},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "xai", accounts
		},
	},
}

// oldCoreGeminiAPIKey is today's core's Gemini API-key block, with a custom base URL.
func oldCoreGeminiAPIKey(upstreamURL string, models, keys []string) map[string]any {
	var ms []any
	for _, m := range models {
		ms = append(ms, map[string]any{"name": m, "alias": m})
	}
	var ks []any
	for _, k := range keys {
		ks = append(ks, map[string]any{"api-key": k})
	}
	return map[string]any{"gemini": []any{map[string]any{
		"name":     "corpus",
		"base-url": upstreamURL,
		"keys":     ks,
		"models":   ms,
	}}}
}

// NewCoreExtensions maps an extension name to its package, relative to the next module root.
var NewCoreExtensions = map[string]string{
	"openai-compatible": "./extensions/openai-compatible",
	"codex":             "./extensions/codex",
	"devin":             "./extensions/devin",
	"claude":            "./extensions/claude",
	"gemini":            "./extensions/gemini",
	"kimi":              "./extensions/kimi",
	"antigravity":       "./extensions/antigravity",
	"meta":              "./extensions/meta",
	"xai":               "./extensions/xai",
}

// BuildNewCore builds the new core and its extensions into dir. moduleRoot is the next/
// folder.
func BuildNewCore(moduleRoot, dir string) error {
	builds := map[string]string{"pluxy": "./cmd/pluxy"}
	for name, pkg := range NewCoreExtensions {
		builds[name] = pkg
	}
	for out, pkg := range builds {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", filepath.Join(dir, out), pkg)
		cmd.Dir = moduleRoot
		if b, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s: %v\n%s", pkg, err, b)
		}
		if out == "pluxy" {
			continue
		}
		manifest := map[string]any{
			"manifest_version": 0,
			"id":               "pluxy/" + out,
			"version":          "0.0.0-corpus",
			"protocol":         []int{0},
			"programs":         map[string]string{runtime.GOOS + "/" + runtime.GOARCH: out},
		}
		b, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, out+".pluxy-extension.json"), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// BuildOldCore builds today's core from coreDir (the repository's core/ folder) to out. The
// old core's plugin host needs cgo.
func BuildOldCore(coreDir, out string) error {
	cmd := exec.Command("go", "build", "-o", out, "./cmd/server")
	cmd.Dir = coreDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("building the old core: %v\n%s", err, b)
	}
	return nil
}

// StartNewCore starts the new core from binDir with the case's provider pointed at upstreamURL,
// one account per key, and the settings in extra added to its config.
func StartNewCore(binDir, workDir, provider, upstreamURL string, models, keys []string, extra map[string]json.RawMessage) (*Core, error) {
	p, ok := Providers[provider]
	if !ok || p.NewCore == nil {
		return nil, fmt.Errorf("no new-core setup for provider %q", provider)
	}
	ext, accounts := p.NewCore(upstreamURL, models, keys)
	cfg := map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{ClientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, ext+".pluxy-extension.json"),
			"accounts": accounts,
		},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	path := filepath.Join(workDir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path)
	cmd.Dir = workDir
	cmd.Env = isolatedEnv(workDir)
	return startProcess(cmd, regexp.MustCompile(`pluxy: listening on (http://\S+)`), "")
}

// OldCoreSetup holds the inputs for one old-core run.
type OldCoreSetup struct {
	Binary      string
	WorkDir     string // empty temporary folder: config, auth folder and HOME go here
	Provider    string
	UpstreamURL string
	Models      []string
	Keys        []string // one upstream key per account
	// ProxyURL and CAFile are the intercepting proxy and the test authority's certificate, for
	// a provider with Intercept set.
	ProxyURL string
	CAFile   string
}

// StartOldCore starts today's core on a free local port with a generated config: the client
// key, one provider entry with one key per account pointed at the fake upstream, no extra retry
// rounds, no waits and no cooldowns, management and the bundled panel off, and the
// embedded model catalog. HOME, the auth folder and the working folder are all inside
// WorkDir, and outbound proxies point at a closed port so the old core's background version
// checks cannot reach the internet; the fake upstream is on loopback, which is never proxied.
func StartOldCore(s OldCoreSetup) (*Core, error) {
	p, ok := Providers[s.Provider]
	if !ok || p.OldCore == nil {
		return nil, fmt.Errorf("no old-core setup for provider %q", s.Provider)
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	authDir := filepath.Join(s.WorkDir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return nil, err
	}
	if p.OldCoreAuthFiles != nil {
		for name, f := range p.OldCoreAuthFiles(s.ProxyURL, s.UpstreamURL, s.Models, s.Keys) {
			b, _ := json.Marshal(f)
			if err := os.WriteFile(filepath.Join(authDir, name), b, 0o600); err != nil {
				return nil, err
			}
		}
	}
	cfg := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"host":                 "127.0.0.1",
			"port":                 port,
			"response-compression": map[string]any{"enabled": false},
		},
		"management": map[string]any{"secret-key": "", "disable-control-panel": true, "disable-auto-update-panel": true},
		"access":     map[string]any{"api-keys": []string{ClientKey}},
		"routing": map[string]any{
			"retry":    map[string]any{"request-retry": 0, "max-retry-interval": 0},
			"cooldown": map[string]any{"disable-cooling": true},
		},
		"oauth":         map[string]any{"auth-dir": authDir},
		"api-keys":      p.OldCore(s.UpstreamURL, s.Models, s.Keys),
		"observability": map[string]any{"usage": map[string]any{"usage-statistics-enabled": false}},
		"plugins":       map[string]any{"enabled": false},
	}
	// YAML is a superset of JSON, so the old core reads this as its YAML config.
	path := filepath.Join(s.WorkDir, "config.yaml")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(s.Binary, "-config", path, "-local-model")
	cmd.Dir = s.WorkDir
	cmd.Env = isolatedEnv(s.WorkDir)
	if p.Intercept {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+s.CAFile)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	core, err := startProcess(cmd, regexp.MustCompile(`API server started successfully on: (\S+)`), "http://"+addr)
	if err != nil || p.OldCoreAuthFiles == nil {
		return core, err
	}
	// The old core loads auth files after it reports that it started; wait until it serves the
	// case's model.
	if err := waitModel(core.URL, s.Models); err != nil {
		core.Stop()
		return nil, fmt.Errorf("%w\ncore output:\n%s", err, core.Logs())
	}
	return core, nil
}

// waitModel waits until the core at url lists every model in models.
func waitModel(url string, models []string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, url+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+ClientKey)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			var list struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&list)
			resp.Body.Close()
			seen := map[string]bool{}
			for _, m := range list.Data {
				seen[m.ID] = true
			}
			all := true
			for _, m := range models {
				all = all && seen[m]
			}
			if all {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the old core did not list %v within 15s of starting", models)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// isolatedEnv is the environment for a core under test: nothing from the caller's
// environment except PATH, and HOME inside the test's folder.
func isolatedEnv(dir string) []string {
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(home, 0o700)
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + dir,
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"NO_PROXY=127.0.0.1,localhost",
	}
}

// Ports for the old core, which takes its port from its config and cannot report one the
// system chose. They lie below the system's ephemeral range (from 32768 on Linux, from 49152 on
// macOS and Windows), where every other listener in the run gets its port: the fake upstreams
// and the new core bind port 0. A port checked free here therefore stays free until the old
// core binds it. A port from the ephemeral range could be handed to a parallel case's listener
// in between; the old core then fails to bind but still reports that it started, and the case
// talks to the other case's server.
const (
	oldCorePortMin = 20000
	oldCorePortMax = 32767
)

var (
	portMu    sync.Mutex
	portsUsed = map[int]bool{} // handed out in this run, never handed out again
)

// freePort returns a free local port for the old core from below the ephemeral range.
func freePort() (int, error) {
	portMu.Lock()
	defer portMu.Unlock()
	for range 1000 {
		port := oldCorePortMin + rand.IntN(oldCorePortMax-oldCorePortMin+1)
		if portsUsed[port] {
			continue
		}
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			continue
		}
		l.Close()
		portsUsed[port] = true
		return port, nil
	}
	return 0, errors.New("corpus: no free port for the old core")
}

// startProcess starts cmd and waits until a line of its output matches ready. The first
// submatch is the base URL unless url is given.
func startProcess(cmd *exec.Cmd, ready *regexp.Regexp, url string) (*Core, error) {
	logs := &logBuffer{}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		pw.Close()
		close(exited)
	}()
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := sc.Text()
			logs.add(line)
			if m := ready.FindStringSubmatch(line); m != nil {
				select {
				case found <- m[1]:
				default:
				}
			}
		}
		_, _ = io.Copy(io.Discard, pr)
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(os.Interrupt)
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-exited
			}
		})
	}
	select {
	case m := <-found:
		if url == "" {
			url = m
		}
		if !strings.HasPrefix(url, "http") {
			url = "http://" + url
		}
		if err := waitListening(url, exited); err != nil {
			stop()
			return nil, fmt.Errorf("%w\n%s", err, logs)
		}
		return &Core{URL: url, stop: stop, logs: logs}, nil
	case <-exited:
		return nil, fmt.Errorf("core exited before it was ready:\n%s", logs)
	case <-time.After(60 * time.Second):
		stop()
		return nil, fmt.Errorf("core was not ready after 60s:\n%s", logs)
	}
}

func waitListening(url string, exited <-chan struct{}) error {
	host := strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return errors.New("core exited")
		default:
		}
		c, err := net.DialTimeout("tcp", host, time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("nothing listening on %s", host)
}

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuffer) add(s string) {
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// corpusTokenExpiry is when the corpus's fake subscription tokens expire: 2100-01-01.
const corpusTokenExpiry = 4102444800

// fakeTokens are a fake ChatGPT sign-in for one corpus account: unsigned JWTs with the claims
// both cores read, deterministic so recordings stay stable. Nothing checks their signatures.
type fakeTokens struct {
	access, id, refresh, accountID, email string
}

func corpusTokens(key string) fakeTokens {
	jwt := func(claims map[string]any) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		b, _ := json.Marshal(claims)
		return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".corpus"
	}
	t := fakeTokens{accountID: "acct-" + key, email: key + "@example.invalid", refresh: "corpus-refresh-" + key}
	t.access = jwt(map[string]any{"exp": corpusTokenExpiry, "sub": "corpus|" + key})
	t.id = jwt(map[string]any{"exp": corpusTokenExpiry, "email": t.email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": t.accountID, "chatgpt_plan_type": "plus"}})
	return t
}

// corpusVertexProject is the Google Cloud project of the corpus's Vertex service accounts.
const corpusVertexProject = "corpus-project"

var (
	corpusRSAOnce sync.Once
	corpusRSAKey  string
)

// corpusRSA returns a PKCS #8 PEM RSA key made for this run, shared by every corpus service
// account: making one takes a moment, and the accounts differ by email.
func corpusRSA() string {
	corpusRSAOnce.Do(func() {
		k, err := rsa.GenerateKey(crand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		der, _ := x509.MarshalPKCS8PrivateKey(k)
		corpusRSAKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	})
	return corpusRSAKey
}

func corpusServiceAccountEmail(i int) string {
	return fmt.Sprintf("corpus-%d@%s.iam.gserviceaccount.com", i+1, corpusVertexProject)
}

// corpusServiceAccount returns service account i's whole JSON key, as today's core's auth file
// holds it, with tokenURI as its token_uri when set (Google's default endpoint otherwise).
func corpusServiceAccount(i int, tokenURI string) map[string]any {
	sa := map[string]any{
		"type":           "service_account",
		"project_id":     corpusVertexProject,
		"private_key_id": "corpus",
		"private_key":    corpusRSA(),
		"client_email":   corpusServiceAccountEmail(i),
	}
	if tokenURI != "" {
		sa["token_uri"] = tokenURI
	}
	return sa
}

// corpusKimiDeviceID is the device ID of the corpus's Kimi accounts.
const corpusKimiDeviceID = "corpus-device-0001"

// corpusAntigravityProject is the Google Cloud project of the corpus's Antigravity accounts.
const corpusAntigravityProject = "corpus-project-0001"
