package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move Kimi's login host only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"auth_base_url":"http://127.0.0.1:9"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://auth.kimi.com"; set.AuthBaseURL != want {
		t.Errorf("outside a test, the login host is %q, want %q", set.AuthBaseURL, want)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthBaseURL != "http://127.0.0.1:9" {
		t.Errorf("under test, the login host is %q, want the settings'", set.AuthBaseURL)
	}
}
