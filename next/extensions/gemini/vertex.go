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
)

// Vertex AI serves Gemini under a Google Cloud project. An account with "vertex" in its settings
// sends its requests there, authorised one of two ways (Google's Vertex AI authentication guide):
//
//   - a service account: credential {"kind": "service_account", "data": {"client_email": ...,
//     "private_key": ...}}, the secret part of the JSON key Google Cloud's console downloads
//     (with token_uri when the key names another token endpoint). The whole key is accepted
//     too, but core redacts every string of a credential from replies, so its project ID and
//     other plain fields would be cut out of them. The extension signs a JWT with the key
//     and trades it at the key's token_uri for an access token (OAuth 2.0 JWT bearer grant, RFC
//     7523), which it keeps until five minutes before it expires. Requests go to
//     {host}/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:{method};
//   - an API key (Vertex AI express mode): credential {"kind": "api_key"}, sent in x-goog-api-key
//     to {host}/v1/publishers/google/models/{model}:{method}.
//
// Settings:
//
//	{"vertex": {"project": "my-project", "location": "us-central1"}, "base_url": "..."}
//
// project defaults to the service account key's project_id when the credential holds the whole
// key; location defaults to global. base_url,
// when set, replaces the Vertex host (https://aiplatform.googleapis.com for global and for API
// keys, https://aiplatform.{us,eu}.rep.googleapis.com for the us and eu multi-regions, otherwise
// https://{location}-aiplatform.googleapis.com). With an API key a base_url that already holds
// /publishers/ is used as the models path itself, and one that ends in a version (v1, v1beta1)
// keeps it.

// CredentialServiceAccount is the credential kind of a Vertex service account key.
const CredentialServiceAccount = "service_account"

// cloudPlatformScope is the OAuth scope Vertex AI takes.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// defaultTokenURI is Google's token endpoint, for a key that names none.
const defaultTokenURI = "https://oauth2.googleapis.com/token"

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

// vertexHost returns the Vertex AI host for a location.
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

func (v *vertexSettings) resolve(ctx context.Context, base string, cred *protocol.Credential, model string, stream bool) (target, error) {
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
		return target{url: u + method(stream), authorize: func(h http.Header) { h.Set("X-Goog-Api-Key", key) }}, nil
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
		u := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s%s", base, url.PathEscape(project), location, m, method(stream))
		return target{url: u, authorize: func(h http.Header) { h.Set("Authorization", "Bearer "+token) }, token: token, sa: sa}, nil
	}
	return target{}, &llm.Error{Code: llm.CodeAuth, Message: "the Vertex account has neither an API key nor a service account key", Scope: llm.ScopeCredential}
}

// serviceAccount is the part of a Google service account JSON key the extension uses.
type serviceAccount struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`

	key *rsa.PrivateKey
}

// readServiceAccount reads and checks a service account key.
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
	if u, err := url.Parse(sa.TokenURI); err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		// The signed assertion goes to token_uri, so it must not cross a network in the clear.
		return nil, bad("has a token_uri that is not an https URL (plain http only to this machine)")
	}
	// Keys pasted through forms often have their newlines escaped.
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

// cacheKey names a service account key in the token cache: its email, token endpoint and a hash
// of its private key, so a replaced key never gets the old key's token.
func (sa *serviceAccount) cacheKey() string {
	h := sha256.Sum256([]byte(sa.PrivateKey))
	return sa.ClientEmail + "|" + sa.TokenURI + "|" + base64.RawURLEncoding.EncodeToString(h[:])
}

// tokenCache keeps each service account's access token until shortly before it expires, and lets
// one call at a time fetch a new one per key. A call waiting for another's fetch can still be
// cancelled. Entries without a usable token are dropped, so failed logins and removed keys do not
// pile up.
type tokenCache struct {
	mu      sync.Mutex
	entries map[string]*tokenEntry
}

type tokenEntry struct {
	users   int           // calls that have the entry; guarded by tokenCache.mu
	turn    chan struct{} // holds one value while a call fetches or reads the token
	token   string
	expires time.Time
}

var tokens = &tokenCache{entries: map[string]*tokenEntry{}}

// tokenLead is how long before its expiry a token is replaced.
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

// invalidate forgets token if it is still the one cached for sa: the upstream refused it, so the
// next call fetches a new one. A newer token fetched meanwhile is kept.
func (c *tokenCache) invalidate(sa *serviceAccount, token string) {
	e := c.enter(sa)
	defer c.leave(e)
	e.turn <- struct{}{}
	if e.token == token {
		e.token = ""
	}
	<-e.turn
}

// enter returns sa's entry, made if needed, counted as in use until leave.
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

// leave ends a call's use of e and drops every entry no call has whose token is gone or expired.
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

// tokenClient fetches tokens and checks keys at login. Its timeout covers only getting a
// credential, never a model request: a token endpoint that stops answering must not hold every
// request of the account. It follows no redirect, which would send the assertion elsewhere.
var tokenClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 30 * time.Second,
		ForceAttemptHTTP2:   true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// fetchToken trades a JWT signed with the service account's key for an access token: Google's
// OAuth 2.0 for server-to-server applications, the JWT bearer grant of RFC 7523.
func fetchToken(ctx context.Context, sa *serviceAccount) (string, time.Time, error) {
	assertion, err := signAssertion(sa, time.Now())
	if err != nil {
		return "", time.Time{}, &llm.Error{Code: llm.CodeInternal, Message: "signing the service account assertion: " + err.Error(), Scope: llm.ScopeCredential}
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sa.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, &llm.Error{Code: llm.CodeAuth, Message: "bad token_uri: " + err.Error(), Scope: llm.ScopeCredential}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := tokenClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", time.Time{}, ctx.Err()
		}
		// No request reached the model, so another account may serve this one.
		return "", time.Time{}, &llm.Error{Code: llm.CodeUnavailable, Message: "service account token endpoint unreachable: " + err.Error(), Scope: llm.ScopeCredential, Retryable: true}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &tr)
	switch {
	case resp.StatusCode == http.StatusOK && tr.AccessToken != "":
		if tr.ExpiresIn <= 0 {
			tr.ExpiresIn = 3600
		}
		return tr.AccessToken, time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second), nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return "", time.Time{}, &llm.Error{Code: llm.CodeUnavailable, Status: resp.StatusCode, Message: fmt.Sprintf("service account token endpoint answered %s", resp.Status), Scope: llm.ScopeCredential, Retryable: true}
	}
	msg := "the token endpoint refused the service account key"
	if tr.Error != "" {
		msg += ": " + tr.Error
		if tr.Description != "" {
			msg += " (" + tr.Description + ")"
		}
	}
	// The key itself was refused (invalid_grant and the like): no request reached the model.
	return "", time.Time{}, &llm.Error{Code: llm.CodeAuth, Message: msg, Scope: llm.ScopeCredential, Retryable: true, ProviderCode: tr.Error}
}

// signAssertion returns the JWT a service account presents to its token endpoint (Google's
// server-to-server OAuth guide): RS256, issued by the account's email for the cloud-platform
// scope, with the token endpoint as audience, valid for an hour.
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

// errNoServiceAccount is returned by a service account login without a key.
var errNoServiceAccount = errors.New("no service account key given")

// loopback reports whether host names this machine.
func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
