package sdk

import (
	"os"

	"github.com/stdotwork/pluxy/next/protocol"
)

// TestLoginHost returns v, a login or token endpoint an account's settings name, only when the
// extension runs under a test: core's environment held protocol.EnvTestLoginHosts=1 when it
// started the extension. Otherwise it returns "", and the extension uses its provider's own hosts.
func TestLoginHost(v string) string {
	if os.Getenv(protocol.EnvTestLoginHosts) == "1" {
		return v
	}
	return ""
}
