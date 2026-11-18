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

// Provider tells the runner how to point the new core at the fake upstream for one provider
// directory under cases/<format>/.
type Provider struct {
	// NewCore returns the new core's extension name (a key of NewCoreExtensions) and its
	// accounts for the provider, one per key. models is empty when the accounts serve any
	// model.
	NewCore func(upstreamURL string, models, keys []string) (extension string, accounts []any)
	// CatalogModels makes the new core's accounts list no models unless a case sets
	// account_models: the extension serves its own catalog. Without it the accounts list the
	// request's model (see newCoreModels). Set for the providers the old core took from auth
	// files rather than from its api-keys block.
	CatalogModels bool
	// Aside, when set, answers the requests the new core makes on its own, outside the case (such
	// as a model list it fetches when it loads an account), so they are not recorded and use up
	// no scripted reply.
	Aside func(r *http.Request) (Reply, bool)
	// Proto, when set, decodes the provider's protobuf requests over Connect into JSON for
	// comparison (see connect.go).
	Proto *ProtoSchema
	// TestLoginHosts starts the new core with PLUXY_TEST_LOGIN_HOSTS=1 in every case, for a
	// provider whose accounts name a login or token endpoint on the fake upstream.
	TestLoginHosts bool
}

// Providers maps a provider directory name to its setup. Later slices add theirs here.
var Providers = map[string]Provider{
	"openai-compatible": {
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
		CatalogModels: true,
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
	// A ChatGPT subscription signed in to Codex that also holds a Sign in with ChatGPT (SIWC)
	// login: in today's core the Codex auth file carries a second token set in a nested "siwc"
	// object and "use_siwc": true, and a request from a client that is not a Codex client goes
	// with that token to https://api.openai.com/v1, a host no setting moves, so the old core
	// runs through the intercepting proxy. In the new core the Codex extension's account holds
	// the SIWC login's credential (the shape the SIWC login produces) and points base_url at
	// the fake upstream's /v1. The SIWC access token differs from the outer one, so an
	// observation shows which of them went upstream.
	"codex-siwc": {
		CatalogModels: true,
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				s := corpusSIWCTokens(k)
				acct := map[string]any{
					"id":       id,
					"settings": map[string]any{"base_url": upstreamURL + "/v1"},
					"credential": map[string]any{"kind": "oauth", "access_token": s.access, "refresh_token": s.refresh,
						"expires_at": corpusTokenExpiry, "data": map[string]any{"siwc": true, "client_id": corpusSIWCClientID,
							"device_id": corpusSIWCDeviceID, "scope": corpusSIWCScope, "id_token": s.id}},
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
	// A Claude subscription login: the Claude extension with an OAuth credential against a Claude
	// auth file in today's core. Today's core always sends a subscription's requests to
	// api.anthropic.com, so only calls that name their URL, such as the management quota cases'
	// api-call relay and the new core's quota call (base_url), reach the fake upstream.
	// The tokens are fake and expire in 2100, so neither core renews them.
	"claude-oauth": {
		CatalogModels: true,
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
						"expires_at": corpusTokenExpiry},
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
	// Gemini's Interactions API with an API key: the Gemini extension with "interactions": true
	// against today's interactions api-keys group, both with a custom base URL. Both send
	// Interactions requests to {upstream}/v1beta/interactions with the key in x-goog-api-key.
	"interactions": {
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id":         id,
					"settings":   map[string]any{"base_url": upstreamURL, "interactions": true},
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
		CatalogModels:  true,
		TestLoginHosts: true, // the key's token_uri is the fake upstream's
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
		CatalogModels: true,
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
		CatalogModels: true,
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
						"expires_at": corpusTokenExpiry, "data": map[string]any{"project_id": corpusAntigravityProject}},
				}
				if len(models) > 0 {
					acct["models"] = models
				}
				accounts = append(accounts, acct)
			}
			return "antigravity", accounts
		},
		// The new core asks Cloud Code which models each Antigravity login has
		// (fetchAvailableModels) when it loads the account; the answer lists the models the cases
		// use. The recordings hold no such request: the old core's were answered the same way.
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
		CatalogModels: true,
		NewCore: func(upstreamURL string, models, keys []string) (string, []any) {
			var accounts []any
			for i, k := range keys {
				id := "corpus"
				if i > 0 {
					id = fmt.Sprintf("corpus-%d", i+1)
				}
				acct := map[string]any{
					"id": id,
					// mint_url is honoured only in a run with management on (StartNewCore): it
					// is where the extension's quota call goes.
					"settings":   map[string]any{"base_url": upstreamURL + "/v1", "mint_url": upstreamURL + "/muse-code/key"},
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
		CatalogModels: true,
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
		Proto: &ProtoSchema{
			Messages: []string{"1", "3", "3.6", "3.10", "8", "10", "15"},
			Repeated: []string{"3", "3.6", "3.10", "10"},
		},
	},
	// A Meta API key: the Meta extension with an api_key credential, a meta group in today's
	// core's api-keys, both sending Responses requests to {base_url}/responses.
	"meta-api-key": {
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
		CatalogModels: true,
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

// StartNewCore starts the new core from binDir with the case's provider pointed at upstreamURL,
// one account per key, the settings in extra added to its config, and the case's override (over)
// merged in. With a managementKey the management API is on, and the extensions honour the login
// and key endpoints their account settings name (a provider's NewCore points them at upstreamURL
// where a quota call needs it).
func StartNewCore(binDir, workDir, provider, upstreamURL string, models, keys []string, extra map[string]json.RawMessage, over *CoreOverride, managementKey string) (*Core, error) {
	p, ok := Providers[provider]
	if !ok || p.NewCore == nil {
		return nil, fmt.Errorf("no new-core setup for provider %q", provider)
	}
	ext, accounts := p.NewCore(upstreamURL, models, keys)
	if over != nil {
		for i, a := range accounts {
			if m, ok := a.(map[string]any); ok {
				Merge(m, roundTrip(over.Account))
				if i < len(over.Accounts) {
					Merge(m, roundTrip(over.Accounts[i]))
				}
			}
		}
	}
	cfg := map[string]any{
		"listen":      "127.0.0.1:0",
		"client_keys": []string{ClientKey},
		"extension": map[string]any{
			"manifest": filepath.Join(binDir, ext+".pluxy-extension.json"),
			"accounts": accounts,
		},
	}
	if managementKey != "" {
		cfg["management_key"] = managementKey
	}
	for k, v := range extra {
		cfg[k] = v
	}
	if over != nil {
		Merge(cfg, roundTrip(over.Config))
	}
	path := filepath.Join(workDir, "config.json")
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(binDir, "pluxy"), "-config", path)
	cmd.Dir = workDir
	cmd.Env = isolatedEnv(workDir)
	if managementKey != "" || p.TestLoginHosts {
		cmd.Env = append(cmd.Env, "PLUXY_TEST_LOGIN_HOSTS=1")
	}
	return startProcess(cmd, regexp.MustCompile(`pluxy: listening on (http://\S+)`), "")
}

// roundTrip returns a deep copy of m, so merging it into several places shares nothing.
func roundTrip(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// isolatedEnv is the environment for a core under test: nothing from the caller's
// environment except PATH, and HOME inside the test's folder. TZ is fixed to UTC, the zone the
// recordings were made in: the old core formatted times in the host's zone. Outbound proxies
// point at a closed local port, so a core's background requests cannot reach the internet; the
// fake upstream is on loopback, which is never proxied.
func isolatedEnv(dir string) []string {
	home := filepath.Join(dir, "home")
	_ = os.MkdirAll(home, 0o700)
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"TZ=UTC",
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
// the new core's extensions read, deterministic so they match the recordings. Nothing checks
// their signatures.
type fakeTokens struct {
	access, id, refresh, accountID, email string
}

// corpusJWT makes an unsigned JWT with the claims.
func corpusJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".corpus"
}

func corpusTokens(key string) fakeTokens {
	jwt := corpusJWT
	t := fakeTokens{accountID: "acct-" + key, email: key + "@example.invalid", refresh: "corpus-refresh-" + key}
	t.access = jwt(map[string]any{"exp": corpusTokenExpiry, "sub": "corpus|" + key})
	t.id = jwt(map[string]any{"exp": corpusTokenExpiry, "email": t.email,
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": t.accountID, "chatgpt_plan_type": "plus"}})
	return t
}

// The corpus's fake Sign in with ChatGPT (SIWC) login, the second token set of a "codex-siwc"
// account (see corpusSIWCTokens).
const (
	corpusSIWCClientID = "corpus-siwc-client"
	corpusSIWCDeviceID = "7f0a3c52-5b1e-4c8d-9a64-2d1e8b6f3a90"
	corpusSIWCScope    = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

// corpusSIWCTokens returns the SIWC login's tokens for one corpus account, made like
// corpusTokens' but with a different subject, so an upstream request shows which token it carried.
func corpusSIWCTokens(key string) fakeTokens {
	t := corpusTokens(key)
	t.refresh = "corpus-siwc-refresh-" + key
	t.access = corpusJWT(map[string]any{"exp": corpusTokenExpiry, "sub": "corpus-siwc|" + key})
	t.id = corpusJWT(map[string]any{"exp": corpusTokenExpiry, "email": t.email,
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

// corpusKimiDeviceID is the device ID of the corpus's Kimi accounts.
const corpusKimiDeviceID = "corpus-device-0001"

// corpusAntigravityProject is the Google Cloud project of the corpus's Antigravity accounts.
const corpusAntigravityProject = "corpus-project-0001"
