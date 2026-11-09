package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move xAI's sign-in only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"auth_issuer":"http://127.0.0.1:9"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthIssuer != defaultIssuer {
		t.Errorf("outside a test, the issuer is %q, want %q", set.AuthIssuer, defaultIssuer)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthIssuer != "http://127.0.0.1:9" {
		t.Errorf("under test, the issuer is %q, want the settings'", set.AuthIssuer)
	}
}
