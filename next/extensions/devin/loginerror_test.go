package main

import (
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func TestLoginErrorTextIsBounded(t *testing.T) {
	sdktest.CheckLoginError(t, "sign-in page error", signInRefusal(sdktest.HostileCode, sdktest.HostileText()), true)
	sdktest.CheckLoginError(t, "long code", signInRefusal(strings.Repeat("a", 5000), ""), false)
}
