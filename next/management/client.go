package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxReply bounds a reply the client reads: the largest replies (logs as JSON) stay well below.
const maxReply = 64 << 20

// Client calls the management API of one core.
type Client struct {
	// URL is the core's base address, such as http://127.0.0.1:8317, without the API's prefix.
	URL string
	// Key is the management key.
	Key string
	// HTTP sends the calls; nil uses a client with a 2-minute timeout.
	HTTP *http.Client
}

// APIError is a call core answered with a status that is not 2xx.
type APIError struct {
	Status int
	// Code and Message are from the reply's error object; a reply without one (POST /reload's
	// refusal is the status) leaves Code empty and Message says the status.
	Code, Message string
	// Body is the reply's body, unchanged.
	Body []byte
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Message + " (" + e.Code + ")"
}

// UnreachableError is a call no core answered: nothing listens at the address, or the
// connection failed before a reply.
type UnreachableError struct {
	URL string
	Err error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("no core answered at %s: %v", e.URL, e.Err)
}
func (e *UnreachableError) Unwrap() error { return e.Err }

var defaultHTTP = &http.Client{Timeout: 2 * time.Minute}

// Call sends one call: method and path under Prefix (such as "GET", "/status"), query
// parameters, and in as the JSON body when it is not nil. A 2xx reply is decoded into out when
// out is not nil. It returns the reply's body unchanged, also with an *APIError.
func (c *Client) Call(ctx context.Context, method, path string, query url.Values, in, out any) ([]byte, error) {
	u := strings.TrimSuffix(c.URL, "/") + Prefix + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &UnreachableError{URL: c.URL, Err: err}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
	if err != nil {
		return nil, &UnreachableError{URL: c.URL, Err: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := &APIError{Status: resp.StatusCode, Body: b}
		var eb ErrorBody
		if json.Unmarshal(b, &eb) == nil && eb.Error.Code != "" {
			e.Code, e.Message = eb.Error.Code, eb.Error.Message
		} else {
			e.Message = fmt.Sprintf("core answered %s", resp.Status)
		}
		return b, e
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return b, fmt.Errorf("reading the reply of %s %s: %w", method, path, err)
		}
	}
	return b, nil
}

// IsUnreachable reports whether err says no core answered.
func IsUnreachable(err error) bool {
	var u *UnreachableError
	return errors.As(err, &u)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	_, err := c.Call(ctx, http.MethodGet, path, q, nil, out)
	return err
}

// Status is GET /status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var v Status
	return v, c.get(ctx, "/status", nil, &v)
}

// Reload is POST /reload. A refused reload is an *APIError with status 422 whose Body is the
// status.
func (c *Client) Reload(ctx context.Context) (Status, error) {
	var v Status
	_, err := c.Call(ctx, http.MethodPost, "/reload", nil, nil, &v)
	return v, err
}

// Accounts is GET /accounts.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var v AccountsReply
	return v.Accounts, c.get(ctx, "/accounts", nil, &v)
}

// SetAccountDisabled is PATCH /accounts/{id} with disabled.
func (c *Client) SetAccountDisabled(ctx context.Context, id string, disabled bool) (Account, error) {
	var v AccountReply
	_, err := c.Call(ctx, http.MethodPatch, "/accounts/"+url.PathEscape(id), nil, AccountPatch{Disabled: &disabled}, &v)
	return v.Account, err
}

// ResetCooldowns is POST /accounts/{id}/cooldown/reset.
func (c *Client) ResetCooldowns(ctx context.Context, id string) (CooldownResetReply, error) {
	var v CooldownResetReply
	_, err := c.Call(ctx, http.MethodPost, "/accounts/"+url.PathEscape(id)+"/cooldown/reset", nil, nil, &v)
	return v, err
}

// TestAccount is POST /accounts/{id}/test; an empty model lets core pick one.
func (c *Client) TestAccount(ctx context.Context, id, model string) (TestResult, error) {
	var v TestReply
	_, err := c.Call(ctx, http.MethodPost, "/accounts/"+url.PathEscape(id)+"/test", nil, TestRequest{Model: model}, &v)
	return v.Result, err
}

// Quota is GET /accounts/{id}/quota.
func (c *Client) Quota(ctx context.Context, id string) (QuotaReply, error) {
	var v QuotaReply
	return v, c.get(ctx, "/accounts/"+url.PathEscape(id)+"/quota", nil, &v)
}

// Packages is GET /packages.
func (c *Client) Packages(ctx context.Context) ([]Package, error) {
	var v PackagesReply
	return v.Packages, c.get(ctx, "/packages", nil, &v)
}

// Registries is GET /registries.
func (c *Client) Registries(ctx context.Context) ([]Registry, error) {
	var v RegistriesReply
	return v.Registries, c.get(ctx, "/registries", nil, &v)
}

// Install is POST /packages.
func (c *Client) Install(ctx context.Context, req InstallRequest) (Package, error) {
	var v PackageReply
	_, err := c.Call(ctx, http.MethodPost, "/packages", nil, req, &v)
	return v.Package, err
}

// SetPackageEnabled is POST /packages/{id}/enable or .../disable; id is publisher/name.
func (c *Client) SetPackageEnabled(ctx context.Context, id string, enabled bool) (Package, error) {
	verb := "disable"
	if enabled {
		verb = "enable"
	}
	var v PackageReply
	_, err := c.Call(ctx, http.MethodPost, PackagePath(id)+"/"+verb, nil, nil, &v)
	return v.Package, err
}

// RemovePackage is DELETE /packages/{id}.
func (c *Client) RemovePackage(ctx context.Context, id string) error {
	_, err := c.Call(ctx, http.MethodDelete, PackagePath(id), nil, nil, nil)
	return err
}

// PackagePath is the path of package id (publisher/name), each part escaped.
func PackagePath(id string) string {
	publisher, name, _ := strings.Cut(id, "/")
	return "/packages/" + url.PathEscape(publisher) + "/" + url.PathEscape(name)
}

// Models is GET /models.
func (c *Client) Models(ctx context.Context) (ModelsReply, error) {
	var v ModelsReply
	return v, c.get(ctx, "/models", nil, &v)
}

// ClientKeys is GET /client-keys.
func (c *Client) ClientKeys(ctx context.Context) ([]ClientKey, error) {
	var v ClientKeysReply
	return v.Keys, c.get(ctx, "/client-keys", nil, &v)
}

// AddClientKey is POST /client-keys; the reply holds the new key.
func (c *Client) AddClientKey(ctx context.Context) (NewClientKeyReply, error) {
	var v NewClientKeyReply
	_, err := c.Call(ctx, http.MethodPost, "/client-keys", nil, nil, &v)
	return v, err
}

// RemoveClientKey is DELETE /client-keys/{id}.
func (c *Client) RemoveClientKey(ctx context.Context, id string) error {
	_, err := c.Call(ctx, http.MethodDelete, "/client-keys/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// Config is GET /config.
func (c *Client) Config(ctx context.Context) (Config, error) {
	var v Config
	return v, c.get(ctx, "/config", nil, &v)
}

// Settings is GET /settings.
func (c *Client) Settings(ctx context.Context) (Settings, error) {
	var v Settings
	return v, c.get(ctx, "/settings", nil, &v)
}

// Logs is GET /logs; q takes after and limit.
func (c *Client) Logs(ctx context.Context, q url.Values) (LogLines, error) {
	var v LogLines
	return v, c.get(ctx, "/logs", q, &v)
}

// Usage is GET /usage; q takes since, until, model, provider, account, key_id, failed,
// bucket and buckets.
func (c *Client) Usage(ctx context.Context, q url.Values) (UsageReply, error) {
	var v UsageReply
	return v, c.get(ctx, "/usage", q, &v)
}

// Setup is GET /setup.
func (c *Client) Setup(ctx context.Context) (Setup, error) {
	var v Setup
	return v, c.get(ctx, "/setup", nil, &v)
}

// SetupScan is POST /setup/scan.
func (c *Client) SetupScan(ctx context.Context) (Setup, error) {
	var v Setup
	_, err := c.Call(ctx, http.MethodPost, "/setup/scan", nil, nil, &v)
	return v, err
}

// SetupApply is POST /setup/apply.
func (c *Client) SetupApply(ctx context.Context, picks []string) (SetupApplyReply, error) {
	var v SetupApplyReply
	_, err := c.Call(ctx, http.MethodPost, "/setup/apply", nil, SetupApplyRequest{Picks: picks}, &v)
	return v, err
}

// SetupDone is POST /setup/done.
func (c *Client) SetupDone(ctx context.Context) (SetupDoneReply, error) {
	var v SetupDoneReply
	_, err := c.Call(ctx, http.MethodPost, "/setup/done", nil, nil, &v)
	return v, err
}
