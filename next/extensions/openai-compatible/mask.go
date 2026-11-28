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
