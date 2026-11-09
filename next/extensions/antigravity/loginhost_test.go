package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move Google's login endpoints only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"auth_base_url":"http://127.0.0.1:9"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.endpoints(); got != (endpoints{googleAuthURL, googleTokenURL, googleUserInfoURL}) {
		t.Errorf("outside a test, the endpoints are %+v, want Google's", got)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.endpoints().token; got != "http://127.0.0.1:9/token" {
		t.Errorf("under test, the token endpoint is %q, want the settings' host", got)
	}
}
