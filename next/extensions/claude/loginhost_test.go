package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move Claude's sign-in page and token endpoint only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"authorize_url":"http://127.0.0.1:9/a","token_url":"http://127.0.0.1:9/t"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthorizeURL != defaultAuthorizeURL || set.TokenURL != defaultTokenURL {
		t.Errorf("outside a test, the endpoints are %q and %q, want %q and %q", set.AuthorizeURL, set.TokenURL, defaultAuthorizeURL, defaultTokenURL)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AuthorizeURL != "http://127.0.0.1:9/a" || set.TokenURL != "http://127.0.0.1:9/t" {
		t.Errorf("under test, the endpoints are %q and %q, want the settings'", set.AuthorizeURL, set.TokenURL)
	}
}
