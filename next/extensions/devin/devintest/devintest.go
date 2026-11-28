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
	"time"

	"github.com/stdotwork/pluxy/next/protocol"
)

const ChatPath = "/exa.api_server_pb.ApiServerService/GetChatMessage"

const StatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

const UserName = "devintest-user"

type Request struct {
	Header  http.Header
	Message []byte
}

type Server struct {
	*httptest.Server

	Chat        func(w http.ResponseWriter, r *http.Request, req Request)
	UserStatus  []byte
	StatusCode  int
	DenyBrowser bool

	mu       sync.Mutex
	n        int
	codes    map[string]string
	tokens   map[string]bool
	requests []Request
	status   []Request
}

func New(t testing.TB) *Server {
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	s := &Server{codes: map[string]string{}, tokens: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Settings() json.RawMessage {
	b, _ := json.Marshal(map[string]string{"base_url": s.URL, "app_base_url": s.URL, "api_base_url": s.URL})
	return b
}

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

func (s *Server) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) StatusRequests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.status...)
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
		writeJSON(w, http.StatusOK, map[string]string{"user_name": UserName, "user_id": UserID, "org_id": "org-1"})
	case r.URL.Path == ChatPath && r.Method == http.MethodPost:
		s.chat(w, r)
	case r.URL.Path == StatusPath && r.Method == http.MethodPost:
		s.userStatus(w, r)
	default:
		http.NotFound(w, r)
	}
}

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

func (s *Server) basicOK(w http.ResponseWriter, r *http.Request) bool {
	basic, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Basic ")
	half := len(basic) / 2
	if len(basic)%2 != 1 || basic[half] != '-' || basic[:half] != basic[half+1:] || !s.valid(basic[:half]) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"code":"unauthenticated","message":"invalid session token"}`)
		return false
	}
	return true
}

func (s *Server) userStatus(w http.ResponseWriter, r *http.Request) {
	if !s.basicOK(w, r) {
		return
	}
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.status = append(s.status, Request{Header: r.Header.Clone(), Message: body})
	s.mu.Unlock()
	if s.StatusCode != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.StatusCode)
		io.WriteString(w, `{"code":"internal","message":"the fake was told to fail"}`)
		return
	}
	reply := s.UserStatus
	if reply == nil {
		reply = DefaultUserStatus()
	}
	w.Header().Set("Content-Type", "application/proto")
	w.Write(reply)
}

const (
	PlanName          = "Pro"
	PlanEnd           = 1796083200
	DailyRemaining    = 70
	WeeklyRemaining   = 40
	DailyResetAtUnix  = 1794268800
	WeeklyResetAtUnix = 1794787200
)

const UserID = "user-1"

func DefaultUserStatus() []byte {
	plan := Bytes(nil, 1, Bytes(nil, 2, []byte(PlanName)))
	plan = Bytes(plan, 3, Varint(nil, 1, PlanEnd))
	plan = Varint(plan, 14, DailyRemaining)
	plan = Varint(plan, 15, WeeklyRemaining)
	plan = Varint(plan, 17, DailyResetAtUnix)
	plan = Varint(plan, 18, WeeklyResetAtUnix)
	us := Bytes(nil, 3, []byte(UserName))
	us = Bytes(us, 7, []byte("user@example.com"))
	us = Bytes(us, 13, plan)
	us = Bytes(us, 36, []byte(UserID))
	return Bytes(nil, 1, us)
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	if !s.basicOK(w, r) {
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

func Reply(w http.ResponseWriter, frames ...[]byte) {
	w.Header().Set("Content-Type", "application/connect+proto")
	for _, f := range frames {
		w.Write(f)
	}
}

func Stream(w http.ResponseWriter, r *http.Request, hold time.Duration, frames ...[]byte) {
	w.Header().Set("Content-Type", "application/connect+proto")
	for _, f := range frames {
		w.Write(f)
		w.(http.Flusher).Flush()
	}
	select {
	case <-time.After(hold):
	case <-r.Context().Done():
	}
}

func Frame(msg []byte) []byte { return connectFrame(0, msg) }

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

func Text(s string) []byte { return Frame(Bytes(nil, 3, []byte(s))) }

func Thinking(s string) []byte { return Frame(Bytes(nil, 9, []byte(s))) }

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

func Usage(in, out uint64) []byte {
	u := Varint(Varint(nil, 2, in), 3, out)
	return Frame(Varint(Bytes(nil, 7, u), 5, 4))
}

func Varint(m []byte, n int, v uint64) []byte {
	m = binary.AppendUvarint(m, uint64(n)<<3)
	return binary.AppendUvarint(m, v)
}

func Bytes(m []byte, n int, b []byte) []byte {
	m = binary.AppendUvarint(m, uint64(n)<<3|2)
	m = binary.AppendUvarint(m, uint64(len(b)))
	return append(m, b...)
}
