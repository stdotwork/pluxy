package geminiwebapi

import (
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// Gemini web endpoints and default headers ----------------------------------
const (
	EndpointGoogle        = "https://www.google.com"
	EndpointInit          = "https://gemini.google.com/app"
	EndpointGenerate      = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
	EndpointRotateCookies = "https://accounts.google.com/RotateCookies"
	EndpointUpload        = "https://content-push.googleapis.com/upload"
)

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

// Model metadata -------------------------------------------------------------
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
	ModelG20Flash = Model{
		Name: "gemini-2.0-flash",
		ModelHeader: http.Header{
			"x-goog-ext-525001261-jspb": []string{"[1,null,null,null,\"f299729663a2343f\"]"},
		},
		AdvancedOnly: false,
	}
	ModelG20FlashThinking = Model{
		Name: "gemini-2.0-flash-thinking",
		ModelHeader: http.Header{
			"x-goog-ext-525001261-jspb": []string{"[null,null,null,null,\"7ca48d02d802f20a\"]"},
		},
		AdvancedOnly: false,
	}
)

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

// Known error codes returned from the server.
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

func AliasFromModelID(modelID string) string {
	return modelID + "-web"
}

// Conversation domain structures -------------------------------------------
type RoleText struct {
	Role string
	Text string
}

type StoredMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

type ConversationRecord struct {
	Model     string          `json:"model"`
	ClientID  string          `json:"client_id"`
	Metadata  []string        `json:"metadata,omitempty"`
	Messages  []StoredMessage `json:"messages"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type Candidate struct {
	RCID            string
	Text            string
	Thoughts        *string
	WebImages       []WebImage
	GeneratedImages []GeneratedImage
}

func (c Candidate) String() string {
	t := c.Text
	if len(t) > 20 {
		t = t[:20] + "..."
	}
	return fmt.Sprintf("Candidate(rcid='%s', text='%s', images=%d)", c.RCID, t, len(c.WebImages)+len(c.GeneratedImages))
}

func (c Candidate) Images() []Image {
	images := make([]Image, 0, len(c.WebImages)+len(c.GeneratedImages))
	for _, wi := range c.WebImages {
		images = append(images, wi.Image)
	}
	for _, gi := range c.GeneratedImages {
		images = append(images, gi.Image)
	}
	return images
}

type ModelOutput struct {
	Metadata   []string
	Candidates []Candidate
	Chosen     int
}

func (m ModelOutput) String() string { return m.Text() }

func (m ModelOutput) Text() string {
	if len(m.Candidates) == 0 {
		return ""
	}
	return m.Candidates[m.Chosen].Text
}

func (m ModelOutput) Thoughts() *string {
	if len(m.Candidates) == 0 {
		return nil
	}
	return m.Candidates[m.Chosen].Thoughts
}

func (m ModelOutput) Images() []Image {
	if len(m.Candidates) == 0 {
		return nil
	}
	return m.Candidates[m.Chosen].Images()
}

func (m ModelOutput) RCID() string {
	if len(m.Candidates) == 0 {
		return ""
	}
	return m.Candidates[m.Chosen].RCID
}

type Gem struct {
	ID          string
	Name        string
	Description *string
	Prompt      *string
	Predefined  bool
}

func (g Gem) String() string {
	return fmt.Sprintf("Gem(id='%s', name='%s', description='%v', prompt='%v', predefined=%v)", g.ID, g.Name, g.Description, g.Prompt, g.Predefined)
}

func decodeHTML(s string) string { return html.UnescapeString(s) }

// Error hierarchy -----------------------------------------------------------
type AuthError struct{ Msg string }

func (e *AuthError) Error() string {
	if e.Msg == "" {
		return "authentication error"
	}
	return e.Msg
}

type APIError struct{ Msg string }

func (e *APIError) Error() string {
	if e.Msg == "" {
		return "api error"
	}
	return e.Msg
}

type ImageGenerationError struct{ APIError }

type GeminiError struct{ Msg string }

func (e *GeminiError) Error() string {
	if e.Msg == "" {
		return "gemini error"
	}
	return e.Msg
}

type TimeoutError struct{ GeminiError }

type UsageLimitExceeded struct{ GeminiError }

type ModelInvalid struct{ GeminiError }

type TemporarilyBlocked struct{ GeminiError }

type ValueError struct{ Msg string }

func (e *ValueError) Error() string {
	if e.Msg == "" {
		return "value error"
	}
	return e.Msg
}
