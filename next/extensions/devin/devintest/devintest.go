// Package devintest is a fake Devin for the Devin extension's tests: its sign-in page
// (/auth/cli/continue), login API (/auth/cli/token, /v3/self) and API server's chat method. It
// issues session tokens of its own and accepts only those.
package devintest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
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

// ChatPath is the API server's chat method.
const ChatPath = "/exa.api_server_pb.ApiServerService/GetChatMessage"

// UserName is the user name the fake's profile reports.
const UserName = "devintest-user"

// Request is a chat request the fake received.
type Request struct {
	Header http.Header
	// Message is the request's protobuf message, out of its Connect frame.
	Message []byte
}

// Server is the fake Devin.
type Server struct {
	*httptest.Server

	// Chat, if set, answers chat requests whose token is valid. The default replies "Hello" in
	// two pieces with usage 11 in, 7 out.
	Chat func(w http.ResponseWriter, r *http.Request, req Request)
	// DenyBrowser makes the sign-in page send the browser back with access_denied.
	DenyBrowser bool

	mu       sync.Mutex
	n        int
	codes    map[string]string // sign-in code to its PKCE challenge
	tokens   map[string]bool
	requests []Request
}

// New starts a fake that is closed when the test ends.
func New(t testing.TB) *Server {
	// The extension honours the fake's address in account settings only under this variable.
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	s := &Server{codes: map[string]string{}, tokens: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Settings returns account settings that point the extension at the server.
func (s *Server) Settings() json.RawMessage {
	b, _ := json.Marshal(map[string]string{"base_url": s.URL, "app_base_url": s.URL, "api_base_url": s.URL})
	return b
}

// Token issues a session token the fake accepts.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token()
}

func (s *Server) token() string {
	s.n++
	t := fmt.Sprintf("devin-session-token$devintest-%d", s.n)
	s.tokens[t] = true
	return t
}

// Revoke makes the fake refuse a token.
func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// Requests returns the chat requests the server answered, in order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/auth/cli/continue" && r.Method == http.MethodGet:
		s.signIn(w, r)
	case r.URL.Path == "/auth/cli/token" && r.Method == http.MethodPost:
		s.exchange(w, r)
	case r.URL.Path == "/v3/self" && r.Method == http.MethodGet:
		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !s.valid(tok) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "Invalid token"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"user_name": UserName, "user_id": "user-1", "org_id": "org-1"})
	case r.URL.Path == ChatPath && r.Method == http.MethodPost:
		s.chat(w, r)
	default:
		http.NotFound(w, r)
	}
}

// signIn plays Devin's sign-in page: it sends the browser back to the redirect URI with a code
// (or access_denied) and the state, or, without a redirect URI, shows the code as the page.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" {
		http.Error(w, "bad sign-in request", http.StatusBadRequest)
		return
	}
	v := url.Values{"state": {q.Get("state")}}
	if s.DenyBrowser {
		v.Set("error", "access_denied")
	} else {
		s.mu.Lock()
		s.n++
		c := fmt.Sprintf("devintest-code-%d", s.n)
		s.codes[c] = q.Get("code_challenge")
		s.mu.Unlock()
		v.Set("code", c)
	}
	back := q.Get("redirect_uri")
	if back == "" {
		if q.Get("cli_pkce_marker") != "1" {
			http.Error(w, "bad sign-in request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, v.Get("code"))
		return
	}
	u, err := url.Parse(back)
	if err != nil || u.Host == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Code     string `json:"code"`
		Verifier string `json:"code_verifier"`
	}
	if json.NewDecoder(r.Body).Decode(&b) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "bad body"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.codes[b.Code]
	delete(s.codes, b.Code)
	sum := sha256.Sum256([]byte(b.Verifier))
	if !ok || challenge != base64.RawURLEncoding.EncodeToString(sum[:]) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "invalid code"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": s.token()})
}

func (s *Server) valid(tok string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokens[tok]
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	basic, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Basic ")
	half := len(basic) / 2
	if len(basic)%2 != 1 || basic[half] != '-' || basic[:half] != basic[half+1:] || !s.valid(basic[:half]) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"unauthenticated","message":"invalid session token"}`)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if len(body) < 5 || body[0] != 0 || int(binary.BigEndian.Uint32(body[1:5])) != len(body)-5 {
		http.Error(w, "bad Connect frame", http.StatusBadRequest)
		return
	}
	req := Request{Header: r.Header.Clone(), Message: body[5:]}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.Chat != nil {
		s.Chat(w, r, req)
		return
	}
	Reply(w, Text("Hel"), Text("lo"), Usage(11, 7), End(""))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Reply writes a streamed chat reply of frames.
func Reply(w http.ResponseWriter, frames ...[]byte) {
	w.Header().Set("Content-Type", "application/connect+proto")
	for _, f := range frames {
		w.Write(f)
	}
}

// Frame wraps a chat reply message in a Connect data frame.
func Frame(msg []byte) []byte { return connectFrame(0, msg) }

// End returns the end frame, carrying an error when code is set.
func End(code string, message ...string) []byte {
	if code == "" {
		return connectFrame(2, []byte("{}"))
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": strings.Join(message, " ")}})
	return connectFrame(2, b)
}

func connectFrame(flags byte, msg []byte) []byte {
	out := make([]byte, 5, 5+len(msg))
	out[0] = flags
	binary.BigEndian.PutUint32(out[1:], uint32(len(msg)))
	return append(out, msg...)
}

// Text returns a frame with a piece of reply text.
func Text(s string) []byte { return Frame(Bytes(nil, 3, []byte(s))) }

// Thinking returns a frame with a piece of reasoning.
func Thinking(s string) []byte { return Frame(Bytes(nil, 9, []byte(s))) }

// ToolCall returns a frame with a piece of a tool call; id and name are left out when empty.
func ToolCall(id, name, args string) []byte {
	var c []byte
	if id != "" {
		c = Bytes(c, 1, []byte(id))
	}
	if name != "" {
		c = Bytes(c, 2, []byte(name))
	}
	if args != "" {
		c = Bytes(c, 3, []byte(args))
	}
	return Frame(Bytes(nil, 6, c))
}

// Usage returns a frame with usage and stop reason 4 (stop).
func Usage(in, out uint64) []byte {
	u := Varint(Varint(nil, 2, in), 3, out)
	return Frame(Varint(Bytes(nil, 7, u), 5, 4))
}

// Varint appends a varint field to a protobuf message.
func Varint(m []byte, n int, v uint64) []byte {
	m = binary.AppendUvarint(m, uint64(n)<<3)
	return binary.AppendUvarint(m, v)
}

// Bytes appends a length-delimited field to a protobuf message.
func Bytes(m []byte, n int, b []byte) []byte {
	m = binary.AppendUvarint(m, uint64(n)<<3|2)
	m = binary.AppendUvarint(m, uint64(len(b)))
	return append(m, b...)
}
