package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move OpenAI's login server only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"auth_base_url":"http://127.0.0.1:9"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.authBase(); got != defaultAuthBaseURL {
		t.Errorf("outside a test, the login server is %q, want %q", got, defaultAuthBaseURL)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := set.authBase(); got != "http://127.0.0.1:9" {
		t.Errorf("under test, the login server is %q, want the settings'", got)
	}
}
