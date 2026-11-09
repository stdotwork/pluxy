package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move Meta's login host and key endpoint only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"auth_base_url":"http://127.0.0.1:9","mint_url":"http://127.0.0.1:9/key"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthBaseURL != defaultAuthBaseURL || set.MintURL != defaultMintURL {
		t.Errorf("outside a test, the hosts are %q and %q, want %q and %q", set.AuthBaseURL, set.MintURL, defaultAuthBaseURL, defaultMintURL)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthBaseURL != "http://127.0.0.1:9" || set.MintURL != "http://127.0.0.1:9/key" {
		t.Errorf("under test, the hosts are %q and %q, want the settings'", set.AuthBaseURL, set.MintURL)
	}
}
