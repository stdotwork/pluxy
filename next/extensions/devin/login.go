package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// Devin's CLI signs in with an authorization code and PKCE (RFC 7636), with no client ID or
// secret: the browser opens {app_base_url}/auth/cli/continue, and the code it comes back with,
// with the PKCE verifier, is traded at {api_base_url}/auth/cli/token for a session token. The
// browser comes back to a loopback address the login serves on this machine; without one (or
// when the user would rather), Devin's page shows the code, and the user pastes it. Today's core
// signs in the same way. The session token does not expire and has no renewal; when Devin
// refuses it, the user signs in again.

var loginMethods = []protocol.LoginMethod{
	{Method: "browser", Kind: protocol.LoginBrowser, Label: "Devin account (browser)"},
	{Method: "token", Kind: protocol.LoginAPIKey, Label: "Devin session token", Inputs: []protocol.LoginInput{
		{Name: "session_token", Label: "Session token (devin-session-token$...)", Secret: true},
	}},
}

// loginWait bounds the wait for the user, as today's core bounds it.
const loginWait = 5 * time.Minute

// authClient talks to Devin's login API. Its timeout covers only signing in, never a model
// request. It follows no redirect, which would send the code or token wherever it points.
var authClient = &http.Client{
	Transport:     sdk.Upstream(http.DefaultTransport.(*http.Transport).Clone()),
	Timeout:       30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func login(ctx context.Context, call sdk.LoginCall, s *sdk.LoginSession) (*protocol.Grant, error) {
	set, err := readSettings(call.Account.Settings)
	if err != nil {
		return nil, err
	}
	switch call.Method {
	case "browser":
		return browserLogin(ctx, set, s)
	case "token":
		tok := formatToken(unquote(call.Input["session_token"]))
		if tok == "" {
			return nil, refused("enter a Devin session token")
		}
		return grant(ctx, set, tok, true)
	}
	return nil, &llm.Error{Code: llm.CodeUnsupported, Message: fmt.Sprintf("no login method %q", call.Method), Scope: llm.ScopeRequest}
}

func browserLogin(ctx context.Context, set settings, s *sdk.LoginSession) (*protocol.Grant, error) {
	ctx, cancel := context.WithTimeout(ctx, loginWait)
	defer cancel()
	verifier := randomBase64(64)
	sum := sha256.Sum256([]byte(verifier))
	state := randomHex(16)
	q := url.Values{
		"state":                 {state},
		"prompt":                {"select_account"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	results := make(chan url.Values, 2)
	listening := false
	if set.CallbackListen == nil || *set.CallbackListen {
		if addr, stop := listenCallback(callbackHandler(state, results)); stop != nil {
			defer stop()
			q.Set("redirect_uri", "http://"+addr+"/callback")
			listening = true
		}
	}
	if !listening {
		// Without a redirect, Devin's page shows the code for the user to paste.
		q.Set("cli_pkce_marker", "1")
	}
	msg := "Open the link and sign in to Devin. If the browser does not come back here, paste the code Devin shows, or the address the browser ended on."
	if !listening {
		msg = "Open the link, sign in to Devin, and paste the code Devin shows."
	}
	prompt := protocol.Prompt{Kind: protocol.LoginBrowser, URL: set.AppBaseURL + "/auth/cli/continue?" + q.Encode(), ExpiresIn: int64(loginWait.Seconds()),
		Message: msg, Inputs: []protocol.LoginInput{{Name: "code", Label: "Code, or the address the browser ended on"}}}
	if err := s.Prompt(prompt); err != nil {
		return nil, err
	}
	inputs, inputErr := forwardInputs(ctx, s.Input)
	for {
		var params url.Values
		select {
		case <-ctx.Done():
			return nil, loginEnded(ctx)
		case err := <-inputErr:
			if ctx.Err() != nil {
				return nil, loginEnded(ctx)
			}
			return nil, err
		case params = <-results:
		case in, ok := <-inputs:
			if !ok {
				// The forwarder stopped; its error, or ctx being done, ends the login.
				inputs = nil
				continue
			}
			p := readPaste(in["code"])
			switch {
			case p.token != "":
				return grant(ctx, set, p.token, false)
			case p.params == nil:
				prompt.Message = "That is not a Devin code or the address the browser ended on; paste the code Devin shows."
				if err := s.Prompt(prompt); err != nil {
					return nil, err
				}
				continue
			}
			params = p.params
		}
		if e := params.Get("error"); e != "" {
			return nil, signInRefusal(e, params.Get("error_description"))
		}
		if st := params.Get("state"); st != "" && st != state {
			prompt.Message = "That address is not the result of this sign-in; open the link, sign in, and paste the code Devin shows."
			if err := s.Prompt(prompt); err != nil {
				return nil, err
			}
			continue
		}
		code := params.Get("code")
		if code == "" {
			return nil, refused("Devin's sign-in came back with no code")
		}
		tok, err := exchange(ctx, set, code, verifier)
		if err != nil {
			return nil, err
		}
		return grant(ctx, set, tok, false)
	}
}

// loginEnded is the error of a login whose context ended: cancelled by core, or out of time.
func loginEnded(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return refused(fmt.Sprintf("the sign-in was not finished within %v; start it again", loginWait))
	}
	return ctx.Err()
}

// paste is what a user pasted: a session token, or a sign-in result as query parameters.
type paste struct {
	token  string
	params url.Values
}

// readPaste reads what the user pasted, as today's core reads it: a session token; an address,
// query or "code=...&state=..." from the sign-in; or a bare code.
func readPaste(s string) paste {
	s = unquote(s)
	switch {
	case s == "":
		return paste{}
	case strings.HasPrefix(s, tokenPrefix) || strings.HasPrefix(s, "eyJ"):
		return paste{token: formatToken(s)}
	}
	if q := queryOf(s); q.Get("code") != "" || q.Get("error") != "" {
		return paste{params: q}
	}
	if !strings.ContainsAny(s, " \t\r\n/?#=") {
		return paste{params: url.Values{"code": {s}}}
	}
	return paste{}
}

// queryOf returns the query (or fragment) parameters in an address or a bare query.
func queryOf(s string) url.Values {
	if u, err := url.Parse(s); err == nil && (u.RawQuery != "" || u.Fragment != "") {
		q := u.Query()
		if f, err := url.ParseQuery(u.Fragment); err == nil {
			for k, v := range f {
				if _, has := q[k]; !has {
					q[k] = v
				}
			}
		}
		return q
	}
	q, _ := url.ParseQuery(strings.TrimPrefix(s, "?"))
	return q
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// callbackHandler answers the browser's return to the loopback address and passes its query on.
func callbackHandler(state string, results chan<- url.Values) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "This page is not the result of the sign-in pluxy started.", http.StatusBadRequest)
			return
		}
		select {
		case results <- q:
		default:
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if q.Get("error") != "" || q.Get("code") == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, "The sign-in failed. You can close this page.")
			return
		}
		io.WriteString(w, "Signed in to Devin. You can close this page.")
	}
}

// listenCallback serves h on a free loopback port and returns its address, or a nil stop when it
// cannot listen.
func listenCallback(h http.HandlerFunc) (addr string, stop func()) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil
	}
	srv := &http.Server{Handler: h, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	return l.Addr().String(), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// forwardInputs reads the login's inputs on its own goroutine, so the login can wait for an
// input and the browser's return at once. It stops, closing inputs, when ctx is done or reading
// fails; a failure is sent on the error channel first.
func forwardInputs(ctx context.Context, input func(context.Context) (map[string]string, error)) (<-chan map[string]string, <-chan error) {
	inputs := make(chan map[string]string, 1)
	inputErr := make(chan error, 1)
	go func() {
		defer close(inputs)
		for {
			in, err := input(ctx)
			if err != nil {
				inputErr <- err
				return
			}
			select {
			case inputs <- in:
			case <-ctx.Done():
				return
			}
		}
	}()
	return inputs, inputErr
}

// exchange trades a sign-in code and the PKCE verifier for a session token.
func exchange(ctx context.Context, set settings, code, verifier string) (string, error) {
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(code), "code_verifier": verifier})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, set.APIBaseURL+"/auth/cli/token", bytes.NewReader(body))
	if err != nil {
		return "", &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad api_base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	status, raw, err := do(ctx, req)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", loginFailure("trading the sign-in code", status)
	}
	var t struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &t) != nil || strings.TrimSpace(t.Token) == "" {
		return "", &llm.Error{Code: llm.CodeUpstream, Message: "Devin's login API sent no session token", Scope: llm.ScopeEndpoint}
	}
	return formatToken(t.Token), nil
}

// grant makes the login's credential, labelled with the account's Devin user name and identified by
// its user ID, which unlike the name does not change. check makes
// Devin's refusal of the token end the login, for a token the user typed; after a sign-in the
// profile is only for the label, as in today's core.
func grant(ctx context.Context, set settings, tok string, check bool) (*protocol.Grant, error) {
	label, userID, err := profile(ctx, set, tok)
	if err != nil && check {
		return nil, err
	}
	g := &protocol.Grant{Credential: &protocol.Credential{Kind: protocol.CredentialAPIKey, APIKey: tok}, Label: label}
	if userID != "" {
		g.Identity = map[string]string{"user_id": userID}
	}
	return g, nil
}

// profile returns the account's label (its user name, else its user ID) and its user ID from
// {api_base_url}/v3/self. Only a refusal of the token is an error; any other failure leaves both
// empty.
func profile(ctx context.Context, set settings, tok string) (label, userID string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, set.APIBaseURL+"/v3/self", nil)
	if err != nil {
		return "", "", nil
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	status, raw, err := do(ctx, req)
	switch {
	case err != nil:
		return "", "", nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "", "", refused("Devin refused the session token; sign in again")
	case status != http.StatusOK:
		return "", "", nil
	}
	var p struct {
		UserName string `json:"user_name"`
		UserID   string `json:"user_id"`
	}
	_ = json.Unmarshal(raw, &p)
	if p.UserName != "" {
		return p.UserName, p.UserID, nil
	}
	return p.UserID, p.UserID, nil
}

// do sends a login request. A request that failed is retryable only when it never reached Devin:
// one that did may have run.
func do(ctx context.Context, req *http.Request) (int, []byte, error) {
	req, sent := sdk.TrackSend(req)
	resp, err := authClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, &llm.Error{Code: llm.CodeUnavailable, Message: "could not reach Devin's login API: " + err.Error(), Retryable: !sent(), Scope: llm.ScopeEndpoint}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, &llm.Error{Code: llm.CodeUnavailable, Message: "reading Devin's login API reply: " + err.Error(), Scope: llm.ScopeEndpoint}
	}
	return resp.StatusCode, raw, nil
}

// loginFailure is the error of a refused login call. Devin's reply body is left out: it is not
// shown to the user, as it may echo the code. A 429 is retryable; a 5xx is not, because the request
// reached Devin and may have run.
func loginFailure(what string, status int) *llm.Error {
	msg := fmt.Sprintf("Devin's login API answered %d %s %s", status, http.StatusText(status), what)
	switch {
	case status == http.StatusTooManyRequests:
		return &llm.Error{Code: llm.CodeRateLimit, Message: msg, Status: status, Retryable: true, Scope: llm.ScopeCredential}
	case status >= 500:
		return &llm.Error{Code: llm.CodeUnavailable, Message: msg, Status: status, Scope: llm.ScopeEndpoint}
	}
	return &llm.Error{Code: llm.CodeAuth, Message: msg + "; sign in again", Status: status, Scope: llm.ScopeCredential}
}

// signInRefusal is the error of a sign-in Devin's page answered with an error. The page's text
// reaches the client before any credential exists for core to redact, so it is bounded and cleaned.
func signInRefusal(code, description string) *llm.Error {
	detail := sdk.LoginCode(code)
	if d := sdk.LoginText(description); d != "" {
		if detail != "" {
			detail += ": "
		}
		detail += d
	}
	return refused("Devin refused the sign-in (" + detail + ")")
}

func refused(msg string) *llm.Error {
	return &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential}
}

func randomBase64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
