package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func TestLoginErrorTextIsBounded(t *testing.T) {
	for _, status := range []int{400, 401, 429, 503} {
		body, _ := json.Marshal(map[string]string{"error": sdktest.HostileCode, "error_description": sdktest.HostileText()})
		sdktest.CheckLoginError(t, "oauth error", loginFailure(status, body, http.Header{}), true)
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": sdktest.HostileCode, "message": sdktest.HostileText()}})
	sdktest.CheckLoginError(t, "api error", loginFailure(400, body, http.Header{}), true)
	long, _ := json.Marshal(map[string]string{"error": strings.Repeat("a", 5000)})
	sdktest.CheckLoginError(t, "long code", loginFailure(400, long, http.Header{}), false)
}
