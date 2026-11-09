// Package oauthtest is a fake OAuth 2.0 provider and model API for the tests of the example oauth
// extension. It serves the device authorization grant (RFC 8628), the authorization code grant with
// PKCE (RFC 6749 and RFC 7636), refresh token rotation with reuse detection (RFC 9700 section
// 4.14.2), and a /v1/chat endpoint that accepts the access tokens it issued.
package oauthtest

import (
	"crypto/rand"
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
	"time"

	"github.com/stdotwork/pluxy/next/conformance"
)

const (
	// deviceInterval is the polling interval the server asks for.
	deviceInterval = time.Second
	// deviceLifetime is how long a device code lasts.
	deviceLifetime = 10 * time.Minute
	// abandonAfter is how long the device logins must stay quiet before DeviceAbandoned closes.
	abandonAfter = 3 * time.Second
	// defaultAccessTTL is the lifetime of an access token when AccessTTL is nil.
	defaultAccessTTL = time.Hour
	// refreshBuffer is how many refresh arrivals RefreshStarted holds for a reader that is behind.
	refreshBuffer = 100
	// deviceGrant is the grant_type of the device authorization grant (RFC 8628 section 3.4).
	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
)

// The device login states.
const (
	statusPending  = "pending"
	statusApproved = "approved"
	statusDenied   = "denied"
)

// The behaviours a token can give at /v1/chat.
const (
	behaviourText        = "text"
	behaviourHang        = "hang"
	behaviourRateLimited = "rate_limited"
	behaviourAuthFailed  = "auth_failed"
)

// Server is a fake provider. Its methods are safe for concurrent use. Its exported fields are set
// before the extension under test sends the server a request.
type Server struct {
	// URL is the server's base URL, the extension's base_url setting.
	URL string
	// DenyBrowser makes the authorization endpoint send the browser back with error=access_denied
	// instead of a code.
	DenyBrowser bool
	// RefreshDelay is how long a refresh request waits before it is answered.
	RefreshDelay time.Duration
	// RefreshFailure makes refresh requests fail: "unavailable" answers 503 temporarily_unavailable,
	// "invalid_grant" answers 400 invalid_grant, and "" answers normally.
	RefreshFailure string
	// AccessTTL returns how long the n-th access token issued by the token endpoint lasts, n counting
	// from 1. Tokens from IssueToken and IssueHang are not counted. Nil means an hour.
	AccessTTL func(n int) time.Duration

	quit chan struct{}

	mu             sync.Mutex
	devices        map[string]*device // by device code
	byUser         map[string]*device // by user code
	codes          map[string]*authCode
	tokens         map[string]*accessToken
	refresh        map[string]*refreshToken
	issued         int
	refreshCalls   int
	refreshStarted chan struct{}
	gate           chan struct{} // open while BlockRefresh holds refresh requests
	chatTokens     []string
	checkTokens    []string
	lastDevice     time.Time // the last request of a device login
	watching       bool
	abandoned      chan struct{}
	abandonOnce    sync.Once
}

// device is one device login: its user code and what the user has done with it.
type device struct {
	userCode string
	expires  time.Time
	status   string
	lastPoll time.Time
	used     bool
}

// authCode is an authorization code, good for one exchange.
type authCode struct {
	challenge   string
	redirectURI string
	used        bool
}

// family is the tokens issued from one login, revoked together.
type family struct{ revoked bool }

// accessToken is a token the model endpoint knows.
type accessToken struct {
	family     *family   // nil for a token from IssueToken or IssueHang
	expires    time.Time // zero: never
	behaviour  string
	cancelled  chan struct{} // closed when a request with this token is cancelled; nil if none
	cancelOnce sync.Once
}

// refreshToken is a refresh token; used once it has been exchanged.
type refreshToken struct {
	family *family
	used   bool
}

// New starts a fake provider for the test and closes it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s := &Server{
		quit:           make(chan struct{}),
		devices:        map[string]*device{},
		byUser:         map[string]*device{},
		codes:          map[string]*authCode{},
		tokens:         map[string]*accessToken{},
		refresh:        map[string]*refreshToken{},
		refreshStarted: make(chan struct{}, refreshBuffer),
		abandoned:      make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/device/code", s.deviceCode)
	mux.HandleFunc("GET /oauth/authorize", s.authorize)
	mux.HandleFunc("POST /oauth/token", s.token)
	mux.HandleFunc("POST /v1/chat", s.chat)
	mux.HandleFunc("GET /v1/me", s.me)
	srv := httptest.NewServer(mux)
	s.URL = srv.URL
	t.Cleanup(func() {
		// Let held requests return before the server waits for them.
		close(s.quit)
		srv.Close()
	})
	return s
}

// ApproveDevice is the user approving the device login that shows userCode; the extension's next
// poll gets tokens. It panics when no device login shows that code.
func (s *Server) ApproveDevice(userCode string) { s.setDevice(userCode, statusApproved) }

// DenyDevice is the user refusing the device login that shows userCode; the extension's next poll
// gets access_denied. It panics when no device login shows that code.
func (s *Server) DenyDevice(userCode string) { s.setDevice(userCode, statusDenied) }

func (s *Server) setDevice(userCode, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.byUser[userCode]
	if !ok {
		panic(fmt.Sprintf("oauthtest: no device login shows user code %q", userCode))
	}
	d.status = status
}

// DeviceAbandoned returns a channel that closes once the device logins have been quiet for three
// seconds: no device code request and no poll since then. An extension that stopped polling after
// its login was cancelled closes it; one that keeps polling never does. The channel closes at most
// once for a server.
func (s *Server) DeviceAbandoned() <-chan struct{} { return s.abandoned }

// checkAbandoned closes abandoned when the device logins have been quiet long enough, and otherwise
// asks again when they would have been. s.mu is not held.
func (s *Server) checkAbandoned() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if left := abandonAfter - time.Since(s.lastDevice); left > 0 {
		time.AfterFunc(left, s.checkAbandoned)
		return
	}
	s.watching = false
	s.abandonOnce.Do(func() { close(s.abandoned) })
}

// IssueToken returns a new access token that the model endpoint answers as behaviour says: "text"
// (the default reply), "hang" (as IssueHang), "rate_limited" or "auth_failed". The token is not tied
// to a refresh token and does not expire. It panics for any other behaviour.
func (s *Server) IssueToken(behaviour string) string {
	switch behaviour {
	case behaviourText, behaviourHang, behaviourRateLimited, behaviourAuthFailed:
	default:
		panic(fmt.Sprintf("oauthtest: unknown token behaviour %q", behaviour))
	}
	tok := randomToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tok] = &accessToken{behaviour: behaviour}
	return tok
}

// IssueHang returns a new access token whose model requests start a reply with one delta, "Hold on",
// and then wait. cancelled closes when such a request is cancelled, so a test can see that the
// extension stopped the upstream request.
func (s *Server) IssueHang() (token string, cancelled <-chan struct{}) {
	ch := make(chan struct{})
	tok := randomToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tok] = &accessToken{behaviour: behaviourHang, cancelled: ch}
	return tok, ch
}

// ChatTokens returns the bearer token of every /v1/chat request so far, in order; "" for a request
// with no bearer token.
func (s *Server) ChatTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.chatTokens...)
}

// CheckTokens returns the bearer token of every /v1/me request (an account check) so far, in
// order.
func (s *Server) CheckTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.checkTokens...)
}

// Grant issues an access token and a refresh token, as a finished login would, for a test that
// needs a credential without driving a login. It counts toward AccessTTL like any token the token
// endpoint issues.
func (s *Server) Grant() (access, refresh string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.issueLocked(nil, time.Now())
	return t.AccessToken, t.RefreshToken
}

// RevokeAccess makes the model endpoint refuse an access token before it expires, as a
// provider does when a token is revoked early. Its refresh token still works.
func (s *Server) RevokeAccess(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, token)
}

// RefreshCalls returns how many refresh requests have arrived.
func (s *Server) RefreshCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshCalls
}

// RefreshStarted returns a channel that receives one value for each refresh request as it arrives,
// including requests held by BlockRefresh. It is buffered; a request that finds the buffer full is
// not reported.
func (s *Server) RefreshStarted() <-chan struct{} { return s.refreshStarted }

// BlockRefresh holds the refresh requests that arrive from now on, without an answer, until release
// is called. Holding is what lets a test keep a refresh in flight while it does something else.
func (s *Server) BlockRefresh() (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if s.gate == gate {
				s.gate = nil
			}
			s.mu.Unlock()
			close(gate)
		})
	}
}

// deviceCode answers a device authorization request (RFC 8628 section 3.2).
func (s *Server) deviceCode(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	dc, uc := randomToken(), userCode()
	d := &device{userCode: uc, expires: now.Add(deviceLifetime), status: statusPending}
	s.mu.Lock()
	s.devices[dc] = d
	s.byUser[uc] = d
	s.lastDevice = now
	if !s.watching {
		s.watching = true
		time.AfterFunc(abandonAfter, s.checkAbandoned)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               dc,
		"user_code":                 uc,
		"verification_uri":          s.URL + "/device",
		"verification_uri_complete": s.URL + "/device?user_code=" + url.QueryEscape(uc),
		"expires_in":                int64(deviceLifetime / time.Second),
		"interval":                  int64(deviceInterval / time.Second),
	})
}

// authorize answers the browser's request for the sign-in page by redirecting it, with a code or
// with access_denied, to the redirect_uri it was given (RFC 6749 section 4.1.2).
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("response_type") != "code" {
		writeError(w, "unsupported_response_type")
		return
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		writeError(w, "invalid_request")
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme == "" {
		writeError(w, "invalid_request")
		return
	}
	out := redirect.Query()
	s.mu.Lock()
	if s.DenyBrowser {
		out.Set("error", "access_denied")
	} else {
		code := randomToken()
		s.codes[code] = &authCode{challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
		out.Set("code", code)
	}
	out.Set("state", q.Get("state"))
	s.mu.Unlock()
	redirect.RawQuery = out.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// token answers the token endpoint, by grant type.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeError(w, "invalid_request")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case deviceGrant:
		s.deviceToken(w, r)
	case "authorization_code":
		s.codeToken(w, r)
	case "refresh_token":
		s.refreshToken(w, r)
	default:
		writeError(w, "unsupported_grant_type")
	}
}

// deviceToken answers a poll of the device authorization grant (RFC 8628 section 3.5).
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[r.PostForm.Get("device_code")]
	if !ok || d.used {
		writeError(w, "invalid_grant")
		return
	}
	s.lastDevice = now
	if now.After(d.expires) {
		writeError(w, "expired_token")
		return
	}
	tooSoon := !d.lastPoll.IsZero() && now.Sub(d.lastPoll) < deviceInterval
	d.lastPoll = now
	switch {
	case tooSoon:
		writeError(w, "slow_down")
	case d.status == statusPending:
		writeError(w, "authorization_pending")
	case d.status == statusDenied:
		writeError(w, "access_denied")
	default:
		d.used = true
		writeJSON(w, http.StatusOK, s.issueLocked(nil, now))
	}
}

// codeToken answers the authorization code grant with PKCE (RFC 7636 section 4.6).
func (s *Server) codeToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[r.PostForm.Get("code")]
	if !ok || c.used {
		writeError(w, "invalid_grant")
		return
	}
	c.used = true // single use, even when this exchange is refused
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if r.PostForm.Get("redirect_uri") != c.redirectURI || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		writeError(w, "invalid_grant")
		return
	}
	writeJSON(w, http.StatusOK, s.issueLocked(nil, time.Now()))
}

// refreshToken answers the refresh token grant (RFC 6749 section 6), rotating the refresh token and
// revoking the grant's family when a used or revoked one comes back (RFC 9700 section 4.14.2).
func (s *Server) refreshToken(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.refreshCalls++
	select {
	case s.refreshStarted <- struct{}{}:
	default:
	}
	gate := s.gate
	s.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		}
	}
	if s.RefreshDelay > 0 {
		select {
		case <-time.After(s.RefreshDelay):
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.RefreshFailure {
	case "unavailable":
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "temporarily_unavailable"})
		return
	case "invalid_grant":
		writeError(w, "invalid_grant")
		return
	}
	rt, ok := s.refresh[r.PostForm.Get("refresh_token")]
	if !ok {
		writeError(w, "invalid_grant")
		return
	}
	if rt.used || rt.family.revoked {
		// A rotated token came back, so a copy of an old grant is in use: revoke the whole family.
		rt.family.revoked = true
		writeError(w, "invalid_grant")
		return
	}
	rt.used = true
	writeJSON(w, http.StatusOK, s.issueLocked(rt.family, time.Now()))
}

// issueLocked mints an access token and a refresh token in fam, or in a new family when fam is nil,
// and returns them as a token set. s.mu must be held.
func (s *Server) issueLocked(fam *family, now time.Time) tokenSet {
	if fam == nil {
		fam = &family{}
	}
	s.issued++
	ttl := defaultAccessTTL
	if s.AccessTTL != nil {
		ttl = s.AccessTTL(s.issued)
	}
	access, refresh := randomToken(), randomToken()
	s.tokens[access] = &accessToken{family: fam, expires: now.Add(ttl), behaviour: behaviourText}
	s.refresh[refresh] = &refreshToken{family: fam}
	return tokenSet{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(ttl / time.Second),
		Email:        "user@example.test",
	}
}

// tokenSet is a successful token reply (RFC 6749 section 5.1).
type tokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Email        string `json:"email"`
}

// chat answers the model endpoint. The reply is newline-delimited JSON, flushed line by line,
// whether or not the request asked to stream.
// me answers whether the bearer token works, as a provider's token check does: 200 with the
// account it belongs to, or 401. It costs nothing and generates nothing.
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	s.checkTokens = append(s.checkTokens, bearer)
	tok, ok := s.tokens[bearer]
	valid := ok && (tok.family == nil || !tok.family.revoked) && (tok.expires.IsZero() || time.Now().Before(tok.expires)) &&
		tok.behaviour != behaviourAuthFailed
	s.mu.Unlock()
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"account": "example"})
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	var bearer string
	if b, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		bearer = b
	}
	s.mu.Lock()
	s.chatTokens = append(s.chatTokens, bearer)
	tok, ok := s.tokens[bearer]
	valid := ok && (tok.family == nil || !tok.family.revoked) && (tok.expires.IsZero() || time.Now().Before(tok.expires))
	s.mu.Unlock()
	if !valid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	// The body ({"model", "stream"}) does not change the reply, so it is only read.
	_, _ = io.Copy(io.Discard, r.Body)

	switch tok.behaviour {
	case behaviourRateLimited:
		w.Header().Set("Retry-After", "20")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
	case behaviourAuthFailed:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth_failed"})
	case behaviourHang:
		send := newLines(w)
		send(map[string]any{"delta": "Hold on"})
		select {
		case <-r.Context().Done():
			tok.cancelOnce.Do(func() {
				if tok.cancelled != nil {
					close(tok.cancelled)
				}
			})
		case <-s.quit:
		}
	default:
		// The default reply is conformance.ReplyText, in two deltas split before " the".
		before, after, _ := strings.Cut(conformance.ReplyText, " the")
		send := newLines(w)
		send(map[string]any{"delta": before})
		send(map[string]any{"delta": " the" + after})
		send(map[string]any{"done": true, "input_tokens": conformance.InputTokens, "output_tokens": conformance.OutputTokens})
	}
}

// newLines starts a newline-delimited JSON reply on w and returns the function that writes one line
// and flushes it.
func newLines(w http.ResponseWriter) func(v any) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	rc := http.NewResponseController(w)
	return func(v any) {
		_ = json.NewEncoder(w).Encode(v)
		_ = rc.Flush()
	}
}

// writeJSON writes v as a JSON reply with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an RFC 6749 section 5.2 error reply with status 400.
func writeError(w http.ResponseWriter, code string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}

// userCode returns a code of eight letters and digits, shaped XXXX-XXXX.
func userCode() string {
	s := strings.ToUpper(rand.Text())
	return s[:4] + "-" + s[4:8]
}

// randomToken returns 32 random bytes, base64url encoded without padding.
func randomToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}
