// Package geminitest is a fake Google for tests of the Gemini extension: the Gemini API with API
// keys, Vertex AI with service account tokens or express mode keys, and the OAuth token endpoint
// a service account trades a signed JWT at. It issues made-up keys and tokens and never talks to
// Google.
//
// Routes:
//
//	POST {prefix}/v1beta/models/{model}:{method}                                       API key
//	GET  {prefix}/v1beta/models                                                         API key
//	POST {prefix}/v1/projects/{p}/locations/{l}/publishers/google/models/{model}:{method} bearer token
//	POST {prefix}/v1/publishers/google/models/{model}:{method}                          API key
//	POST /token                                                                         JWT bearer grant
package geminitest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// Request is a GenerateContent request the server received with a valid credential.
type Request struct {
	// Kind is "gemini" (the Gemini API), "vertex" (a project path with a bearer token) or
	// "vertex-key" (Vertex express mode).
	Kind    string
	Prefix  string
	Project string
	// Location is the location in a project path.
	Location string
	Model    string
	Method   string
	Stream   bool
	Header   http.Header
	Body     []byte
}

// Server is the fake. Set its fields before requests arrive.
type Server struct {
	*httptest.Server

	// Generate, if set, answers GenerateContent requests whose credential is valid. The
	// default replies "Hello" (streamed for streamGenerateContent) with usage 11 in, 7 out.
	Generate func(w http.ResponseWriter, r *http.Request, req Request)
	// TokenDown makes the token endpoint answer 503.
	TokenDown bool

	mu       sync.Mutex
	n        int
	keys     map[string]bool
	tokens   map[string]bool
	accounts map[string]*rsa.PublicKey // service account email to its public key
	requests []Request
	grants   int
}

// New starts a fake that is closed when the test ends.
func New(t testing.TB) *Server {
	s := &Server{keys: map[string]bool{}, tokens: map[string]bool{}, accounts: map[string]*rsa.PublicKey{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// IssueKey returns a new API key the server accepts.
func (s *Server) IssueKey() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	k := fmt.Sprintf("geminitest-key-%d", s.n)
	s.keys[k] = true
	return k
}

// Requests returns the GenerateContent requests the server answered, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Grants returns how many access tokens the token endpoint has issued.
func (s *Server) Grants() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.grants
}

var (
	keyOnce sync.Once
	testKey *rsa.PrivateKey
)

// rsaKey returns one RSA key for every service account the package makes: generating one is
// slow, and the accounts differ by email.
func rsaKey() *rsa.PrivateKey {
	keyOnce.Do(func() {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		testKey = k
	})
	return testKey
}

// ServiceAccount returns a service account JSON key, for project, whose token_uri is the
// server's token endpoint. With accepted false the server does not know the account and refuses
// its JWT.
func (s *Server) ServiceAccount(project string, accepted bool) []byte {
	k := rsaKey()
	s.mu.Lock()
	s.n++
	email := fmt.Sprintf("sa-%d@%s.iam.gserviceaccount.example", s.n, project)
	if accepted {
		s.accounts[email] = &k.PublicKey
	}
	s.mu.Unlock()
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	b, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     project,
		"private_key_id": "geminitest",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   email,
		"token_uri":      s.URL + "/token",
	})
	return b
}

// Revoke makes the server refuse an access token or API key it issued.
func (s *Server) Revoke(credential string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, credential)
	delete(s.keys, credential)
}

var (
	geminiPath    = regexp.MustCompile(`^(.*)/v1beta/models/([^/:]+):([A-Za-z]+)$`)
	modelsPath    = regexp.MustCompile(`^(.*)/v1beta/models$`)
	vertexPath    = regexp.MustCompile(`^(.*)/v1/projects/([^/]+)/locations/([^/]+)/publishers/google/models/([^/:]+):([A-Za-z]+)$`)
	vertexKeyPath = regexp.MustCompile(`^(.*)/v1/publishers/google/models/([^/:]+):([A-Za-z]+)$`)
)

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/token" && r.Method == http.MethodPost {
		s.token(w, r)
		return
	}
	if m := modelsPath.FindStringSubmatch(p); m != nil && r.Method == http.MethodGet {
		if !s.validKey(r) {
			badKey(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"models":[{"name":"models/gemini-test"}]}`)
		return
	}
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	var req Request
	switch {
	case vertexPath.MatchString(p):
		m := vertexPath.FindStringSubmatch(p)
		if !s.validToken(r) {
			googleError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Request had invalid authentication credentials.", "")
			return
		}
		req = Request{Kind: "vertex", Prefix: m[1], Project: m[2], Location: m[3], Model: m[4], Method: m[5]}
	case vertexKeyPath.MatchString(p):
		m := vertexKeyPath.FindStringSubmatch(p)
		if !s.validKey(r) {
			badKey(w)
			return
		}
		req = Request{Kind: "vertex-key", Prefix: m[1], Model: m[2], Method: m[3]}
	case geminiPath.MatchString(p):
		m := geminiPath.FindStringSubmatch(p)
		if !s.validKey(r) {
			badKey(w)
			return
		}
		req = Request{Kind: "gemini", Prefix: m[1], Model: m[2], Method: m[3]}
	default:
		http.NotFound(w, r)
		return
	}
	req.Stream = req.Method == "streamGenerateContent"
	req.Header = r.Header.Clone()
	req.Body, _ = io.ReadAll(r.Body)
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.Generate != nil {
		s.Generate(w, r, req)
		return
	}
	if req.Stream {
		StreamText(w, "Hel", "lo")
		return
	}
	JSONText(w, "Hello")
}

func (s *Server) validKey(r *http.Request) bool {
	k := r.Header.Get("X-Goog-Api-Key")
	if k == "" {
		k = r.URL.Query().Get("key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[k]
}

func (s *Server) validToken(r *http.Request) bool {
	t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	return ok && s.tokens[t]
}

// token answers a JWT bearer grant (RFC 7523) as Google's token endpoint does.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if s.TokenDown {
		http.Error(w, "token endpoint down", http.StatusServiceUnavailable)
		return
	}
	_ = r.ParseForm()
	refuse := func(desc string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":"invalid_grant","error_description":%q}`, desc)
	}
	if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		refuse("unsupported grant_type")
		return
	}
	parts := strings.Split(r.PostForm.Get("assertion"), ".")
	if len(parts) != 3 {
		refuse("Invalid JWT.")
		return
	}
	var claims struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Iat   int64  `json:"iat"`
		Exp   int64  `json:"exp"`
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(hb, &header) != nil || header.Alg != "RS256" || header.Typ != "JWT" {
		refuse("Invalid JWT header.")
		return
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(cb, &claims) != nil {
		refuse("Invalid JWT.")
		return
	}
	s.mu.Lock()
	pub := s.accounts[claims.Iss]
	s.mu.Unlock()
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if pub == nil || err != nil || rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig) != nil {
		refuse("Invalid JWT Signature.")
		return
	}
	now := time.Now().Unix()
	if claims.Aud != s.URL+"/token" || claims.Scope != "https://www.googleapis.com/auth/cloud-platform" ||
		claims.Iat > now+60 || claims.Exp <= now || claims.Exp-claims.Iat > 3600 {
		refuse("Invalid JWT: bad audience, scope or lifetime.")
		return
	}
	s.mu.Lock()
	s.n++
	s.grants++
	tok := fmt.Sprintf("geminitest-token-%d", s.n)
	s.tokens[tok] = true
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":%q,"expires_in":3599,"token_type":"Bearer"}`, tok)
}

func badKey(w http.ResponseWriter) {
	googleError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "API key not valid. Please pass a valid API key.", "API_KEY_INVALID")
}

func googleError(w http.ResponseWriter, status int, code, msg, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	details := "[]"
	if reason != "" {
		details = fmt.Sprintf(`[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":%q,"domain":"googleapis.com"}]`, reason)
	}
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q,"status":%q,"details":%s}}`, status, msg, code, details)
}

// RateLimited writes Google's 429 with a RetryInfo delay of seconds.
func RateLimited(w http.ResponseWriter, seconds int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	fmt.Fprintf(w, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED",`+
		`"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"%ds"}]}}`, seconds)
}

// Usage is the usage the default replies report.
const Usage = `{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}`

// StreamText writes a streamGenerateContent reply: one chunk per piece of text, the last with
// finishReason STOP and Usage.
func StreamText(w http.ResponseWriter, pieces ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for i, p := range pieces {
		t, _ := json.Marshal(p)
		finish, usage := "", ""
		if i == len(pieces)-1 {
			finish, usage = `,"finishReason":"STOP"`, `,"usageMetadata":`+Usage
		}
		fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":%s}]}%s,\"index\":0}]%s,\"modelVersion\":\"gemini-test\",\"responseId\":\"geminitest\"}\n\n", t, finish, usage)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
}

// JSONText writes a generateContent reply holding text.
func JSONText(w http.ResponseWriter, text string) {
	t, _ := json.Marshal(text)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":%s}]},"finishReason":"STOP","index":0}],"usageMetadata":%s,"modelVersion":"gemini-test","responseId":"geminitest"}`, t, Usage)
}
