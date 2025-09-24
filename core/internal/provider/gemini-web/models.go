package geminiwebapi

import (
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// Endpoints used by the Gemini web app
const (
	EndpointGoogle        = "https://www.google.com"
	EndpointInit          = "https://gemini.google.com/app"
	EndpointGenerate      = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
	EndpointRotateCookies = "https://accounts.google.com/RotateCookies"
	EndpointUpload        = "https://content-push.googleapis.com/upload"
)

// Default headers
var (
	HeadersGemini = http.Header{
		"Content-Type":  []string{"application/x-www-form-urlencoded;charset=utf-8"},
		"Host":          []string{"gemini.google.com"},
		"Origin":        []string{"https://gemini.google.com"},
		"Referer":       []string{"https://gemini.google.com/"},
		"User-Agent":    []string{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"},
		"X-Same-Domain": []string{"1"},
	}
	HeadersRotateCookies = http.Header{
		"Content-Type": []string{"application/json"},
	}
	HeadersUpload = http.Header{
		"Push-ID": []string{"feeds/mcudyrk2a4khkz"},
	}
)

// Model defines available model names and headers
type Model struct {
	Name         string
	ModelHeader  http.Header
	AdvancedOnly bool
}

var (
	ModelUnspecified = Model{
		Name:         "unspecified",
		ModelHeader:  http.Header{},
		AdvancedOnly: false,
	}
	ModelG25Flash = Model{
		Name: "gemini-2.5-flash",
		ModelHeader: http.Header{
			"x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"71c2d248d3b102ff\",null,null,0,[4]]"},
		},
		AdvancedOnly: false,
	}
	ModelG25Pro = Model{
		Name: "gemini-2.5-pro",
		ModelHeader: http.Header{
			"x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"4af6c7f5da75d65d\",null,null,0,[4]]"},
		},
		AdvancedOnly: false,
	}
	ModelG20Flash = Model{ // Deprecated, still supported
		Name: "gemini-2.0-flash",
		ModelHeader: http.Header{
			"x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"f299729663a2343f\"]"},
		},
		AdvancedOnly: false,
	}
	ModelG20FlashThinking = Model{ // Deprecated, still supported
		Name: "gemini-2.0-flash-thinking",
		ModelHeader: http.Header{
			"x-goog-ext-525001261-jspb": []string{"[null,null,null,null,\"7ca48d02d802f20a\"]"},
		},
		AdvancedOnly: false,
	}
)

// ModelFromName returns a model by name or error if not found
func ModelFromName(name string) (Model, error) {
	switch name {
	case ModelUnspecified.Name:
		return ModelUnspecified, nil
	case ModelG25Flash.Name:
		return ModelG25Flash, nil
	case ModelG25Pro.Name:
		return ModelG25Pro, nil
	case ModelG20Flash.Name:
		return ModelG20Flash, nil
	case ModelG20FlashThinking.Name:
		return ModelG20FlashThinking, nil
	default:
		return Model{}, &ValueError{Msg: "Unknown model name: " + name}
	}
}

// Known error codes returned from server
const (
	ErrorUsageLimitExceeded   = 1037
	ErrorModelInconsistent    = 1050
	ErrorModelHeaderInvalid   = 1052
	ErrorIPTemporarilyBlocked = 1060
)

var (
	GeminiWebAliasOnce sync.Once
	GeminiWebAliasMap  map[string]string
)

// EnsureGeminiWebAliasMap initializes alias lookup lazily.
func EnsureGeminiWebAliasMap() {
	GeminiWebAliasOnce.Do(func() {
		GeminiWebAliasMap = make(map[string]string)
		for _, m := range registry.GetGeminiModels() {
			if m.ID == "gemini-2.5-flash-lite" {
				continue
			} else if m.ID == "gemini-2.5-flash" {
				GeminiWebAliasMap["gemini-2.5-flash-image-preview"] = "gemini-2.5-flash"
			}
			alias := AliasFromModelID(m.ID)
			GeminiWebAliasMap[strings.ToLower(alias)] = strings.ToLower(m.ID)
		}
	})
}

// GetGeminiWebAliasedModels returns Gemini models exposed with web aliases.
func GetGeminiWebAliasedModels() []*registry.ModelInfo {
	EnsureGeminiWebAliasMap()
	aliased := make([]*registry.ModelInfo, 0)
	for _, m := range registry.GetGeminiModels() {
		if m.ID == "gemini-2.5-flash-lite" {
			continue
		} else if m.ID == "gemini-2.5-flash" {
			cpy := *m
			cpy.ID = "gemini-2.5-flash-image-preview"
			cpy.Name = "gemini-2.5-flash-image-preview"
			cpy.DisplayName = "Nano Banana"
			cpy.Description = "Gemini 2.5 Flash Preview Image"
			aliased = append(aliased, &cpy)
		}
		cpy := *m
		cpy.ID = AliasFromModelID(m.ID)
		cpy.Name = cpy.ID
		aliased = append(aliased, &cpy)
	}
	return aliased
}

// MapAliasToUnderlying normalizes web aliases back to canonical Gemini IDs.
func MapAliasToUnderlying(name string) string {
	EnsureGeminiWebAliasMap()
	n := strings.ToLower(name)
	if u, ok := GeminiWebAliasMap[n]; ok {
		return u
	}
	const suffix = "-web"
	if strings.HasSuffix(n, suffix) {
		return strings.TrimSuffix(n, suffix)
	}
	return name
}

// AliasFromModelID builds the web alias for a Gemini model identifier.
func AliasFromModelID(modelID string) string {
	return modelID + "-web"
}
