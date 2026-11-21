package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
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
		return b, apiError(resp, b)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return b, fmt.Errorf("reading the reply of %s %s: %w", method, path, err)
		}
	}
	return b, nil
}

// apiError is the error for a non-2xx reply whose body is b.
func apiError(resp *http.Response, b []byte) *APIError {
	e := &APIError{Status: resp.StatusCode, Body: b}
	var eb ErrorBody
	if json.Unmarshal(b, &eb) == nil && eb.Error.Code != "" {
		e.Code, e.Message = eb.Error.Code, eb.Error.Message
	} else {
		e.Message = fmt.Sprintf("core answered %s", resp.Status)
	}
	return e
}

// Stream sends one call like Call, without a body, and copies a 2xx reply's body to w as it
// arrives, however large it is: Call stops reading at 64 MiB, which would cut a log file short
// with no error. A non-2xx reply is the same *APIError Call gives, and nothing is written. A
// reply that breaks off is an *UnreachableError, after the bytes that arrived were written;
// an error of w is returned as it is.
//
// The client's overall timeout does not bound the copy, since a large file takes as long as it
// takes: it bounds the wait for the reply to begin, and ctx ends the copy.
func (c *Client) Stream(ctx context.Context, method, path string, query url.Values, w io.Writer) error {
	u := strings.TrimSuffix(c.URL, "/") + Prefix + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	hc := defaultHTTP
	if c.HTTP != nil {
		hc = c.HTTP
	}
	wait := hc.Timeout
	whole := *hc
	whole.Timeout = 0
	callerCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	var timer *time.Timer
	if wait > 0 {
		timer = time.AfterFunc(wait, cancel)
	}
	resp, err := whole.Do(req)
	if timer != nil {
		timer.Stop()
	}
	if err != nil {
		if callerCtx.Err() != nil {
			return callerCtx.Err()
		}
		return &UnreachableError{URL: c.URL, Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxReply))
		if err != nil {
			return &UnreachableError{URL: c.URL, Err: err}
		}
		return apiError(resp, b)
	}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			if callerCtx.Err() != nil {
				return callerCtx.Err()
			}
			return &UnreachableError{URL: c.URL, Err: rerr}
		}
	}
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
	err := c.get(ctx, "/status", nil, &v)
	return v, err
}

// Accounts is GET /accounts.
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var v AccountsReply
	err := c.get(ctx, "/accounts", nil, &v)
	return v.Accounts, err
}

// ChainPath is the path of name's chain, its name escaped as one path segment.
func ChainPath(name string) string { return "/chains/" + url.PathEscape(name) }

// Packages is GET /packages.
func (c *Client) Packages(ctx context.Context) ([]Package, error) {
	var v PackagesReply
	err := c.get(ctx, "/packages", nil, &v)
	return v.Packages, err
}

// PackagePath is the path of package id (publisher/name), each part escaped.
func PackagePath(id string) string {
	publisher, name, _ := strings.Cut(id, "/")
	return "/packages/" + url.PathEscape(publisher) + "/" + url.PathEscape(name)
}

// ClientKeys is GET /client-keys.
func (c *Client) ClientKeys(ctx context.Context) ([]ClientKey, error) {
	var v ClientKeysReply
	err := c.get(ctx, "/client-keys", nil, &v)
	return v.Keys, err
}

// MultipartBody is a ready multipart/form-data body, made by NewMultipartBody.
type MultipartBody struct {
	bytes       []byte
	contentType string
}

// NewMultipartBody makes a multipart/form-data body: one file part named field holding data as
// filename, and the fields name=value.
func NewMultipartBody(field, filename string, data []byte, fields map[string]string) (*MultipartBody, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, err := w.CreateFormFile(field, filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if err := w.WriteField(name, fields[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return &MultipartBody{bytes: b.Bytes(), contentType: w.FormDataContentType()}, nil
}

// Setup is GET /setup.
func (c *Client) Setup(ctx context.Context) (Setup, error) {
	var v Setup
	err := c.get(ctx, "/setup", nil, &v)
	return v, err
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

// ClientPaste is GET /setup/clients/{id}/paste. keyID, when not empty, names the client key
// the reply's Key placeholder points at.
func (c *Client) ClientPaste(ctx context.Context, id, keyID string) (ClientPaste, error) {
	var q url.Values
	if keyID != "" {
		q = url.Values{"key_id": {keyID}}
	}
	var v ClientPaste
	err := c.get(ctx, "/setup/clients/"+url.PathEscape(id)+"/paste", q, &v)
	return v, err
}

// SetupDone is POST /setup/done.
func (c *Client) SetupDone(ctx context.Context) (SetupDoneReply, error) {
	var v SetupDoneReply
	_, err := c.Call(ctx, http.MethodPost, "/setup/done", nil, nil, &v)
	return v, err
}

// ThemePath is the path of theme id (publisher/name), each part escaped.
func ThemePath(id string) string {
	publisher, name, _ := strings.Cut(id, "/")
	return "/themes/" + url.PathEscape(publisher) + "/" + url.PathEscape(name)
}

// CallBytes sends one call with raw JSON bytes body (if non-nil) and behaves like Call.
func (c *Client) CallBytes(ctx context.Context, method, path string, query url.Values, raw []byte, out any) ([]byte, error) {
	return c.callRaw(ctx, method, path, query, "application/json", raw, out)
}

// CallForm sends one call with a multipart/form-data body (form) and behaves like Call.
func (c *Client) CallForm(ctx context.Context, method, path string, query url.Values, form *MultipartBody, out any) ([]byte, error) {
	return c.callRaw(ctx, method, path, query, form.contentType, form.bytes, out)
}

// callRaw sends one call with body and its content type.
func (c *Client) callRaw(ctx context.Context, method, path string, query url.Values, contentType string, raw []byte, out any) ([]byte, error) {
	u := strings.TrimSuffix(c.URL, "/") + Prefix + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if raw != nil && contentType != "" {
		req.Header.Set("Content-Type", contentType)
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
