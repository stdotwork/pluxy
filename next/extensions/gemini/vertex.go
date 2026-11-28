package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

const CredentialServiceAccount = "service_account"

const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

const defaultTokenURI = "https://oauth2.googleapis.com/token"

// googleTokenURIs are the token endpoints the keys Google Cloud makes for its standard universe
// name: today's, and the one older keys name. A key is matched exactly, as written. Keys for a
// sovereign-cloud universe name that universe's endpoint and are not supported.
var googleTokenURIs = map[string]bool{
	defaultTokenURI: true,
	"https://accounts.google.com/o/oauth2/token": true,
}

type vertexSettings struct {
	Project  string `json:"project"`
	Location string `json:"location"`
}

var locationPattern = regexp.MustCompile(`^[a-z0-9-]*$`)

func (v *vertexSettings) check() error {
	if !locationPattern.MatchString(v.Location) {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "vertex.location must be a Google Cloud location such as us-central1 or global", Scope: llm.ScopeCredential}
	}
	if strings.ContainsAny(v.Project, "/?#") {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "vertex.project must be a Google Cloud project ID", Scope: llm.ScopeCredential}
	}
	return nil
}

func vertexHost(location string) string {
	switch location {
	case "", "global":
		return "https://aiplatform.googleapis.com"
	case "us", "eu":
		return "https://aiplatform." + location + ".rep.googleapis.com"
	}
	return "https://" + location + "-aiplatform.googleapis.com"
}

var versionSegment = regexp.MustCompile(`/v\d+(?:beta\d*|alpha\d*)?$`)

func (v *vertexSettings) resolve(ctx context.Context, base string, cred *protocol.Credential, model, method string) (target, error) {
	m := url.PathEscape(model)
	switch {
	case cred != nil && cred.Kind == protocol.CredentialAPIKey && cred.APIKey != "":
		if base == "" {
			base = "https://aiplatform.googleapis.com"
		}
		var u string
		switch {
		case strings.Contains(base, "/publishers/"):
			u = base + "/" + m
		case versionSegment.MatchString(base):
			u = base + "/publishers/google/models/" + m
		default:
			u = base + "/v1/publishers/google/models/" + m
		}
		key := cred.APIKey
		return target{url: u + method, authorize: func(h http.Header) { h.Set("X-Goog-Api-Key", key) }}, nil
	case cred != nil && cred.Kind == CredentialServiceAccount:
		sa, err := readServiceAccount(cred.Data)
		if err != nil {
			return target{}, err
		}
		project := v.Project
		if project == "" {
			project = sa.ProjectID
		}
		if project == "" {
			return target{}, &llm.Error{Code: llm.CodeInvalidRequest, Message: "the account needs vertex.project: its service account key names no project_id", Scope: llm.ScopeCredential}
		}
		location := v.Location
		if location == "" {
			location = "global"
		}
		if base == "" {
			base = vertexHost(location)
		}
		token, err := tokens.get(ctx, sa)
		if err != nil {
			return target{}, err
		}
		u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s%s", base, url.PathEscape(project), location, m, method)
		return target{url: u, authorize: func(h http.Header) { h.Set("Authorization", "Bearer "+token) }, token: token, sa: sa}, nil
	}
	return target{}, &llm.Error{Code: llm.CodeAuth, Message: "the Vertex account has neither an API key nor a service account key", Scope: llm.ScopeCredential}
}

type serviceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`

	key *rsa.PrivateKey
}

func readServiceAccount(raw json.RawMessage) (*serviceAccount, error) {
	bad := func(msg string) error {
		return &llm.Error{Code: llm.CodeAuth, Message: "the service account key " + msg, Scope: llm.ScopeCredential}
	}
	var sa serviceAccount
	if len(raw) == 0 || json.Unmarshal(raw, &sa) != nil {
		return nil, bad("is not a JSON object")
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return nil, bad(fmt.Sprintf("has type %q, not service_account", sa.Type))
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, bad("needs client_email and private_key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = defaultTokenURI
	}
	if !googleTokenURIs[sa.TokenURI] {
		if sdk.TestLoginHost(sa.TokenURI) == "" {
			return nil, bad("has a token_uri that is not Google's token endpoint (" + defaultTokenURI + ")")
		}
		if u, err := url.Parse(sa.TokenURI); err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
			return nil, bad("has a token_uri that is not an https URL (plain http only to this machine)")
		}
	}
	block, _ := pem.Decode([]byte(strings.ReplaceAll(sa.PrivateKey, `\n`, "\n")))
	if block == nil {
		return nil, bad("has a private_key that is not PEM")
	}
	var key any
	var err error
	if key, err = x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	rk, ok := key.(*rsa.PrivateKey)
	if err != nil || !ok {
		return nil, bad("has a private_key that is not an RSA key")
	}
	sa.key = rk
	return &sa, nil
}

func (sa *serviceAccount) cacheKey() string {
	h := sha256.Sum256([]byte(sa.PrivateKey))
	return sa.ClientEmail + "|" + sa.TokenURI + "|" + base64.RawURLEncoding.EncodeToString(h[:])
}

type tokenCache struct {
	mu      sync.Mutex
	entries map[string]*tokenEntry
}

type tokenEntry struct {
	users   int
	turn    chan struct{}
	token   string
	expires time.Time
}

var tokens = &tokenCache{entries: map[string]*tokenEntry{}}

const tokenLead = 5 * time.Minute

func (c *tokenCache) get(ctx context.Context, sa *serviceAccount) (string, error) {
	e := c.enter(sa)
	defer c.leave(e)
	select {
	case e.turn <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-e.turn }()
	if e.token != "" && time.Until(e.expires) > tokenLead {
		return e.token, nil
	}
	token, expires, err := fetchToken(ctx, sa)
	if err != nil {
		e.token = ""
		return "", err
	}
	e.token, e.expires = token, expires
	return token, nil
}

func (c *tokenCache) invalidate(sa *serviceAccount, token string) {
	e := c.enter(sa)
	defer c.leave(e)
	e.turn <- struct{}{}
	if e.token == token {
		e.token = ""
	}
	<-e.turn
}

func (c *tokenCache) enter(sa *serviceAccount) *tokenEntry {
	key := sa.cacheKey()
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[key]
	if e == nil {
		e = &tokenEntry{turn: make(chan struct{}, 1)}
		c.entries[key] = e
	}
	e.users++
	return e
}

func (c *tokenCache) leave(e *tokenEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.users--
	now := time.Now()
	for k, e := range c.entries {
		if e.users == 0 && (e.token == "" || !now.Before(e.expires)) {
			delete(c.entries, k)
		}
	}
}

var tokenClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: sdk.Upstream(&http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
	}),
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func fetchToken(ctx context.Context, sa *serviceAccount) (string, time.Time, error) {
	assertion, err := signAssertion(sa, time.Now())
	if err != nil {
		return "", time.Time{}, &llm.Error{Code: llm.CodeInternal, Message: "signing the service account assertion: " + err.Error(), Scope: llm.ScopeCredential}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(sdk.Uncaptured(ctx), http.MethodPost, sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, &llm.Error{Code: llm.CodeAuth, Message: "bad token_uri: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := tokenClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", time.Time{}, ctx.Err()
		}
		return "", time.Time{}, &llm.Error{Code: llm.CodeUnavailable, Message: "service account token endpoint unreachable: " + err.Error(), Scope: llm.ScopeCredential, Retryable: true}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		if ctx.Err() != nil {
			return "", time.Time{}, ctx.Err()
		}
		return "", time.Time{}, &llm.Error{Code: llm.CodeUnavailable, Message: "service account token endpoint reply broke off: " + err.Error(), Scope: llm.ScopeCredential, Retryable: true}
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &tr)
	switch {
	case resp.StatusCode == http.StatusOK && tr.AccessToken != "":
		life := sdk.BoundSeconds(tr.ExpiresIn, sdk.MaxTokenLife)
		if life == 0 {
			life = time.Hour
		}
		return tr.AccessToken, time.Now().Add(life), nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return "", time.Time{}, &llm.Error{Code: llm.CodeUnavailable, Status: resp.StatusCode, Message: fmt.Sprintf("service account token endpoint answered %s", resp.Status), Scope: llm.ScopeCredential, Retryable: true}
	case resp.StatusCode == http.StatusOK:
		return "", time.Time{}, &llm.Error{Code: llm.CodeUpstream, Message: "service account token endpoint sent no access token", Scope: llm.ScopeCredential, Retryable: true}
	}
	code := sdk.LoginCode(tr.Error)
	msg := "the token endpoint refused the service account key"
	if code != "" {
		msg += ": " + code
		if d := sdk.LoginText(tr.Description); d != "" {
			msg += " (" + d + ")"
		}
	}
	return "", time.Time{}, &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential, Retryable: true, ProviderCode: code}
}

func signAssertion(sa *serviceAccount, now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if sa.PrivateKeyID != "" {
		header["kid"] = sa.PrivateKeyID
	}
	claims := map[string]any{
		"iss":   sa.ClientEmail,
		"scope": cloudPlatformScope,
		"aud":   sa.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, sa.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

var errNoServiceAccount = errors.New("no service account key given")

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
