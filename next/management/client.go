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

const maxReply = 64 << 20

type Client struct {
	URL  string
	Key  string
	HTTP *http.Client
}

type APIError struct {
	Status        int
	Code, Message string
	Body          []byte
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Message + " (" + e.Code + ")"
}

type UnreachableError struct {
	URL string
	Err error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("no core answered at %s: %v", e.URL, e.Err)
}
func (e *UnreachableError) Unwrap() error { return e.Err }

var defaultHTTP = &http.Client{Timeout: 2 * time.Minute}

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

func IsUnreachable(err error) bool {
	var u *UnreachableError
	return errors.As(err, &u)
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	_, err := c.Call(ctx, http.MethodGet, path, q, nil, out)
	return err
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var v Status
	err := c.get(ctx, "/status", nil, &v)
	return v, err
}

func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var v AccountsReply
	err := c.get(ctx, "/accounts", nil, &v)
	return v.Accounts, err
}

func ChainPath(name string) string { return "/chains/" + url.PathEscape(name) }

func (c *Client) Packages(ctx context.Context) ([]Package, error) {
	var v PackagesReply
	err := c.get(ctx, "/packages", nil, &v)
	return v.Packages, err
}

func PackagePath(id string) string {
	publisher, name, _ := strings.Cut(id, "/")
	return "/packages/" + url.PathEscape(publisher) + "/" + url.PathEscape(name)
}

func (c *Client) ClientKeys(ctx context.Context) ([]ClientKey, error) {
	var v ClientKeysReply
	err := c.get(ctx, "/client-keys", nil, &v)
	return v.Keys, err
}

func (c *Client) Requests(ctx context.Context) ([]RunningRequest, error) {
	var v RequestsReply
	err := c.get(ctx, "/requests", nil, &v)
	return v.Requests, err
}

func RequestCancelPath(trace string) string { return "/requests/" + url.PathEscape(trace) + "/cancel" }

func (c *Client) CancelRequest(ctx context.Context, trace string) (RunningRequest, error) {
	var v RequestReply
	_, err := c.Call(ctx, http.MethodPost, RequestCancelPath(trace), nil, nil, &v)
	return v.Request, err
}

type MultipartBody struct {
	bytes       []byte
	contentType string
}

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

func (c *Client) Setup(ctx context.Context) (Setup, error) {
	var v Setup
	err := c.get(ctx, "/setup", nil, &v)
	return v, err
}

func (c *Client) SetupScan(ctx context.Context) (Setup, error) {
	var v Setup
	_, err := c.Call(ctx, http.MethodPost, "/setup/scan", nil, nil, &v)
	return v, err
}

func (c *Client) SetupApply(ctx context.Context, picks []string) (SetupApplyReply, error) {
	var v SetupApplyReply
	_, err := c.Call(ctx, http.MethodPost, "/setup/apply", nil, SetupApplyRequest{Picks: picks}, &v)
	return v, err
}

func (c *Client) ClientPaste(ctx context.Context, id, keyID, model string) (ClientPaste, error) {
	q := url.Values{}
	if keyID != "" {
		q.Set("key_id", keyID)
	}
	if model != "" {
		q.Set("model", model)
	}
	var v ClientPaste
	err := c.get(ctx, "/setup/clients/"+url.PathEscape(id)+"/paste", q, &v)
	return v, err
}

func (c *Client) ConnectClient(ctx context.Context, id, label, model string) (ClientConnection, error) {
	fields := map[string]string{}
	if label != "" {
		fields["label"] = label
	}
	if model != "" {
		fields["model"] = model
	}
	var body any
	if len(fields) > 0 {
		body = fields
	}
	var v ClientConnection
	_, err := c.Call(ctx, http.MethodPost, "/setup/clients/"+url.PathEscape(id)+"/connect", nil, body, &v)
	return v, err
}

func (c *Client) DisconnectClient(ctx context.Context, id string) (ClientConnection, error) {
	var v ClientConnection
	_, err := c.Call(ctx, http.MethodPost, "/setup/clients/"+url.PathEscape(id)+"/disconnect", nil, nil, &v)
	return v, err
}

func (c *Client) SetupDone(ctx context.Context) (SetupDoneReply, error) {
	var v SetupDoneReply
	_, err := c.Call(ctx, http.MethodPost, "/setup/done", nil, nil, &v)
	return v, err
}

func ThemePath(id string) string {
	publisher, name, _ := strings.Cut(id, "/")
	return "/themes/" + url.PathEscape(publisher) + "/" + url.PathEscape(name)
}

func (c *Client) CallBytes(ctx context.Context, method, path string, query url.Values, raw []byte, out any) ([]byte, error) {
	return c.callRaw(ctx, method, path, query, "application/json", raw, out)
}

func (c *Client) CallForm(ctx context.Context, method, path string, query url.Values, form *MultipartBody, out any) ([]byte, error) {
	return c.callRaw(ctx, method, path, query, form.contentType, form.bytes, out)
}

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
