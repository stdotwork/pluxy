package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func TestLoginErrorTextIsBounded(t *testing.T) {
	long, _ := json.Marshal(map[string]string{"error": strings.Repeat("a", 5000)})
	sdktest.CheckLoginError(t, "long code", loginFailure(400, long), false)
	withJWT, _ := json.Marshal(map[string]any{"error": map[string]string{"code": sdktest.HostileJWT}})
	sdktest.CheckLoginError(t, "token as code", loginFailure(400, withJWT), false)
}

func TestCallbackErrorTextIsBounded(t *testing.T) {
	_, _, err := signInResult(url.Values{"state": {"s"}, "error": {sdktest.HostileCode + ": " + sdktest.HostileText()}}, "s")
	sdktest.CheckLoginError(t, "callback error", err, true)
}
