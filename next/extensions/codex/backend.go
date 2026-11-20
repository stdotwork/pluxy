package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/sdk"
)

// backendCall is the Native.Body of a codex.backend call, as core writes it
// (next/internal/server/codexbackend.go).
type backendCall struct {
	Method string `json:"method"`
	// Path is the request path below /backend-api, escaped as the client sent it.
	Path        string `json:"path"`
	Query       string `json:"query"`
	ContentType string `json:"content_type"`
	Body        []byte `json:"body"`
}

// backendHeaders are the client's request headers a backend call carries on (lower-case, as the
// call's native headers name them): those alpha search passes.
var backendHeaders = alphaSearchHeaders

// backend serves a Codex CLI call to the ChatGPT backend beyond generation and search: the
// call's method, path, query and body go to the same host as the account's generation requests
// (its base_url without the /codex the generation path adds), with the account's token and
// ChatGPT account, and the backend's reply comes back whole. Only a ChatGPT subscription has such
// a backend; core decides which paths the Codex CLI may reach, and the extension holds the call
// to a path below the backend's root.
func backend(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
	s, err := readSettings(call.Account.Settings)
	if err != nil {
		return err
	}
	a, err := accountOf(call.Account.Credential)
	if err != nil {
		return err
	}
	if !a.subscription {
		return &llm.Error{Code: llm.CodeUnsupported, Scope: llm.ScopeCredential, Retryable: true,
			Message: "an API key has no ChatGPT backend to pass a Codex CLI call to"}
	}
	req := call.Request
	var c backendCall
	if err := json.Unmarshal(req.Native.Body, &c); err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the backend call is not understood: " + err.Error(), Scope: llm.ScopeRequest}
	}
	switch c.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut:
	default:
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: fmt.Sprintf("the backend call's method %q is not passed on", c.Method), Scope: llm.ScopeRequest}
	}
	if !backendPathOK(c.Path) {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "the backend call's path is not passed on", Scope: llm.ScopeRequest}
	}
	target := strings.TrimSuffix(baseURL(s, a), "/codex") + c.Path
	if c.Query != "" {
		target += "?" + c.Query
	}
	var body io.Reader
	if len(c.Body) > 0 {
		body = bytes.NewReader(c.Body)
	}
	hreq, err := http.NewRequestWithContext(ctx, c.Method, target, body)
	if err != nil {
		return &llm.Error{Code: llm.CodeInvalidRequest, Message: "bad base_url: " + err.Error(), Scope: llm.ScopeCredential}
	}
	h := hreq.Header
	if c.ContentType != "" && body != nil {
		h.Set("Content-Type", c.ContentType)
	}
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Bearer "+a.token)
	for _, name := range backendHeaders {
		if v := req.Native.Headers[name]; v != "" {
			h.Set(name, v)
		}
	}
	setIdentity(h, req.Native.Headers, s, a)
	resp, err := client.Do(hreq)
	if err != nil {
		return requestFailed(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return endpointStatusError(resp, "")
	}
	reply, err := readReply(ctx, resp, "backend")
	if err != nil {
		return err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	if err := out.StartBody(llm.FormatCodexBackend, contentType); err != nil {
		return err
	}
	if err := out.Body(string(reply)); err != nil {
		return err
	}
	return out.Finish(llm.Finish{Reason: llm.FinishStop}, nil)
}

// backendPathOK reports whether path is a request path below the backend's root as core writes
// it: a slash first, and no segment that is empty, "." or "..", or that names a slash, a
// backslash or a control character once unescaped. The Codex CLI's calls are allowlisted in core;
// this holds a call, whatever sent it, below the root.
func backendPathOK(path string) bool {
	if !strings.HasPrefix(path, "/") {
		return false
	}
	for _, seg := range strings.Split(path[1:], "/") {
		s, err := url.PathUnescape(seg)
		if err != nil || s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\") {
			return false
		}
		for _, c := range s {
			if c < 0x20 || c == 0x7f {
				return false
			}
		}
	}
	return true
}
