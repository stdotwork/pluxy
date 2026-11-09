package sdk

import (
	"testing"

	"github.com/stdotwork/pluxy/next/protocol"
)

func TestTestLoginHost(t *testing.T) {
	t.Setenv(protocol.EnvTestLoginHosts, "")
	if got := TestLoginHost("http://127.0.0.1:1"); got != "" {
		t.Errorf("without the test variable, TestLoginHost = %q, want empty", got)
	}
	t.Setenv(protocol.EnvTestLoginHosts, "1")
	if got := TestLoginHost("http://127.0.0.1:1"); got != "http://127.0.0.1:1" {
		t.Errorf("with the test variable, TestLoginHost = %q", got)
	}
}
