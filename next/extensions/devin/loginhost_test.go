package main

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

// A stored account's settings must not decide where a login's codes and a refresh token go: they
// move Devin's sign-in page and login API only in a test run, which sets protocol.EnvTestLoginHosts.
func TestSettingsMoveLoginHostOnlyUnderTest(t *testing.T) {
	const raw = `{"app_base_url":"http://127.0.0.1:9","api_base_url":"http://127.0.0.1:9"}`
	t.Setenv(protocol.EnvTestLoginHosts, "")
	set, err := readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AppBaseURL != defaultAppBaseURL || set.APIBaseURL != defaultAPIBaseURL {
		t.Errorf("outside a test, the hosts are %q and %q, want %q and %q", set.AppBaseURL, set.APIBaseURL, defaultAppBaseURL, defaultAPIBaseURL)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	set, err = readSettings([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if set.AppBaseURL != "http://127.0.0.1:9" || set.APIBaseURL != "http://127.0.0.1:9" {
		t.Errorf("under test, the hosts are %q and %q, want the settings'", set.AppBaseURL, set.APIBaseURL)
	}
}
