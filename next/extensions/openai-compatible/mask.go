package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
	"github.com/stdotwork/pluxy/next/sdk"
)

// An account can point at Google's own host, and Google's errors — or a connection error quoting a
// Vertex base_url — name the Google project or account of the owner's upstream account, which a
// client holding a pluxy key must not learn. maskGoogle wraps the handlers whose errors reach
// clients (Generate, CountTokens and Endpoint) and masks a returned error when its raw body has
// Google's error shape or the account's base_url host is googleapis.com or ends in .googleapis.com
// (passing any project in that URL's /projects/<id>/ path as a known name), after the handler
// classified it. This covers an error status, an error inside a stream, and a bodyless error such
// as an unreachable upstream. Login, Check, Quota and Models are the owner's, who reads them to
// find which project needs an API enabled, and are not wrapped. An error from another vendor's
// host with a body of another shape is left byte-identical.
func maskGoogle(h sdk.Handler) sdk.Handler {
	return func(ctx context.Context, call sdk.Call, out *sdk.Stream) error {
		err := h(ctx, call, out)
		if err == nil {
			return nil
		}
		isGoogle, known := googleBaseURL(call.Account)
		var e *llm.Error
		if errors.As(err, &e) {
			if isGoogle || geminigenerate.IsGoogleErrorShape(e.Raw) {
				return geminigenerate.MaskProject(e, known...)
			}
			return err
		}
		if !isGoogle {
			return err
		}
		m := geminigenerate.MaskProject(&llm.Error{Code: llm.CodeInternal, Message: err.Error()}, known...)
		if m.Message == err.Error() {
			return err
		}
		if ctx.Err() != nil {
			m.Code = llm.CodeCancelled
		}
		return m
	}
}

// googleBaseURL reports whether the account's base_url host is googleapis.com or a subdomain of
// it, and returns the project in its /projects/<id>/ path, if any, as known names to mask (both
// the decoded and escaped forms). When base_url has no scheme, it is parsed with https://.
func googleBaseURL(a protocol.Account) (bool, []string) {
	if len(a.Settings) == 0 {
		return false, nil
	}
	var s struct {
		BaseURL string `json:"base_url"`
	}
	if json.Unmarshal(a.Settings, &s) != nil || s.BaseURL == "" {
		return false, nil
	}
	raw := strings.TrimSpace(s.BaseURL)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false, nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host != "googleapis.com" && !strings.HasSuffix(host, ".googleapis.com") {
		return false, nil
	}
	parts := strings.Split(u.Path, "/")
	escapedParts := strings.Split(u.EscapedPath(), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "projects" && parts[i+1] != "" {
			decoded := parts[i+1]
			known := []string{decoded}
			if i+1 < len(escapedParts) && escapedParts[i+1] != "" && escapedParts[i+1] != decoded {
				known = append(known, escapedParts[i+1])
			}
			return true, known
		}
	}
	return true, nil
}
