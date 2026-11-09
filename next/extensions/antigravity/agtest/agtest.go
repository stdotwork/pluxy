// Package agtest is a fake Google for tests of the Antigravity extension: Google's OAuth sign-in
// (/o/oauth2/v2/auth), token endpoint (/token) and user info (/oauth2/v2/userinfo), and Cloud
// Code's loadCodeAssist, onboardUser and generateContent methods (/v1internal:*). It issues
// made-up tokens and never talks to Google.
package agtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// ClientSecret is the client secret the fake accepts.
const ClientSecret = "agtest-client-secret"

// Email is the e-mail address of the fake's signed-in user.
const Email = "user@example.com"

// Project is the Google Cloud project the fake gives every account.
const Project = "agtest-project"

// Request is a Cloud Code request the server answered with a valid token.
type Request struct {
	Path   string // path and query
	Header http.Header
	Body   []byte
}

// Server is the fake. Set its fields before requests arrive.
type Server struct {
	*httptest.Server

	// Generate, if set, answers generate requests whose token is valid. The default replies
	// "Hello" (streamed when asked) with usage 11 in, 7 out.
	Generate func(w http.ResponseWriter, r *http.Request, req Request)
	// DenyBrowser makes the sign-in page send the browser back with access_denied.
	DenyBrowser bool
	// TokenDown makes the token endpoint answer 503.
	TokenDown bool
	// NoProject makes loadCodeAssist name no project, so the account must be onboarded;
	// onboarding takes OnboardPolls polls before it is done.
	NoProject    bool
	OnboardPolls int
	// Answer, if set, sees every request first; it answers one by returning true.
	Answer func(w http.ResponseWriter, r *http.Request) bool

	mu       sync.Mutex
	n        int
	codes    map[string]code // authorization code to what it was issued for
	access   map[string]bool
	refresh  map[string]bool
	requests []Request
	onboards int
}

type code struct{ redirect, challenge string }

// New starts a fake that is closed when the test ends.
func New(t testing.TB) *Server {
	// The extension honours the fake's address in account settings only under this variable.
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	s := &Server{codes: map[string]code{}, access: map[string]bool{}, refresh: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Settings returns account settings that point the extension at the server. The login does not
// listen for the browser's return, so tests never bind its fixed port.
func (s *Server) Settings() json.RawMessage {
	b, _ := json.Marshal(map[string]any{"base_url": s.URL, "auth_base_url": s.URL, "callback_listen": false})
	return b
}

// Grant returns a new access and refresh token the server accepts.
func (s *Server) Grant() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grant()
}

func (s *Server) grant() (string, string) {
	s.n++
	a, r := fmt.Sprintf("agtest-access-%d", s.n), fmt.Sprintf("agtest-refresh-%d", s.n)
	s.access[a], s.refresh[r] = true, true
	return a, r
}

// Revoke makes the server refuse a token it issued.
func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.access, token)
	delete(s.refresh, token)
}

// Requests returns the Cloud Code requests the server answered, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if s.Answer != nil && s.Answer(w, r) {
		return
	}
	switch {
	case r.URL.Path == "/o/oauth2/v2/auth" && r.Method == http.MethodGet:
		s.authorize(w, r)
	case r.URL.Path == "/token" && r.Method == http.MethodPost:
		s.token(w, r)
	case r.URL.Path == "/oauth2/v2/userinfo" && r.Method == http.MethodGet:
		if !s.valid(r) {
			writeJSON(w, http.StatusUnauthorized, googleError(401, "Invalid Credentials", "UNAUTHENTICATED"))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"email": Email, "verified_email": true})
	case strings.HasPrefix(r.URL.Path, "/v1internal:") && r.Method == http.MethodPost:
		s.cloudCode(w, r)
	default:
		http.NotFound(w, r)
	}
}

// authorize plays Google's sign-in page: it sends the browser back to the redirect URI with a
// code (or access_denied) and the state.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Host == "" || q.Get("response_type") != "code" || q.Get("client_id") == "" {
		http.Error(w, "bad sign-in request", http.StatusBadRequest)
		return
	}
	v := url.Values{"state": {q.Get("state")}}
	if s.DenyBrowser {
		v.Set("error", "access_denied")
	} else {
		s.mu.Lock()
		s.n++
		c := fmt.Sprintf("agtest-code-%d", s.n)
		s.codes[c] = code{redirect: q.Get("redirect_uri"), challenge: q.Get("code_challenge")}
		s.mu.Unlock()
		v.Set("code", c)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.TokenDown {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	f := r.PostForm
	if f.Get("client_secret") != ClientSecret || f.Get("client_id") == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "The OAuth client was not found."})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch f.Get("grant_type") {
	case "authorization_code":
		c, ok := s.codes[f.Get("code")]
		delete(s.codes, f.Get("code"))
		sum := sha256.Sum256([]byte(f.Get("code_verifier")))
		if !ok || c.redirect != f.Get("redirect_uri") || (c.challenge != "" && c.challenge != base64.RawURLEncoding.EncodeToString(sum[:])) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Bad Request"})
			return
		}
		a, rt := s.grant()
		writeJSON(w, http.StatusOK, map[string]any{"access_token": a, "refresh_token": rt, "expires_in": 3599, "token_type": "Bearer"})
	case "refresh_token":
		if !s.refresh[f.Get("refresh_token")] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "Token has been expired or revoked."})
			return
		}
		// Google keeps the refresh token and sends none back.
		a, _ := s.grant()
		writeJSON(w, http.StatusOK, map[string]any{"access_token": a, "expires_in": 3599, "token_type": "Bearer"})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (s *Server) valid(r *http.Request) bool {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.access[tok]
}

func (s *Server) cloudCode(w http.ResponseWriter, r *http.Request) {
	if !s.valid(r) {
		writeJSON(w, http.StatusUnauthorized, googleError(401, "Request had invalid authentication credentials.", "UNAUTHENTICATED"))
		return
	}
	req := Request{Path: r.URL.RequestURI(), Header: r.Header.Clone()}
	req.Body, _ = io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	switch r.URL.Path {
	case "/v1internal:loadCodeAssist":
		if s.NoProject {
			writeJSON(w, http.StatusOK, map[string]any{"allowedTiers": []any{map[string]any{"id": "standard-tier", "isDefault": true}}})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cloudaicompanionProject": Project, "currentTier": map[string]string{"id": "free-tier"}})
	case "/v1internal:onboardUser":
		s.mu.Lock()
		s.onboards++
		done := s.onboards > s.OnboardPolls
		s.mu.Unlock()
		if !done {
			writeJSON(w, http.StatusOK, map[string]any{"name": "operations/onboard", "done": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": "operations/onboard", "done": true,
			"response": map[string]any{"cloudaicompanionProject": map[string]string{"id": Project}}})
	case "/v1internal:generateContent", "/v1internal:streamGenerateContent":
		if s.Generate != nil {
			s.Generate(w, r, req)
			return
		}
		if r.URL.Path == "/v1internal:streamGenerateContent" {
			StreamText(w, "Hel", "lo")
			return
		}
		JSONText(w, "Hello")
	default:
		http.NotFound(w, r)
	}
}

func googleError(code int, msg, status string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg, "status": status}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Usage is the usage the default replies report.
const Usage = `{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}`

// StreamText writes a streamed GenerateContent reply in Cloud Code's envelope: one event per
// piece, the last with the finish reason and usage.
func StreamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for i, p := range pieces {
		t, _ := json.Marshal(p)
		end := ""
		if i == len(pieces)-1 {
			end = `,"finishReason":"STOP"`
		}
		usage := ""
		if end != "" {
			usage = `,"usageMetadata":` + Usage
		}
		fmt.Fprintf(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":%s}]},\"index\":0%s}]%s,\"modelVersion\":\"agtest\",\"responseId\":\"agtest\"},\"traceId\":\"agtest-trace\"}\n\n", t, end, usage)
	}
}

// JSONText writes a GenerateContent reply holding text, in Cloud Code's envelope.
func JSONText(w http.ResponseWriter, text string) {
	t, _ := json.Marshal(text)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":%s}]},"finishReason":"STOP","index":0}],"usageMetadata":%s,"modelVersion":"agtest","responseId":"agtest"},"traceId":"agtest-trace"}`, t, Usage)
}
