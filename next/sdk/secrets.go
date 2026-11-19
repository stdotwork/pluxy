package sdk

import "github.com/stdotwork/pluxy/next/protocol"

// AccountSecrets returns the secrets of account a for UpstreamText, as protocol.AccountSecrets
// does: everything Upstream sends or core holds as secret, by its secret parts.
func AccountSecrets(a protocol.Account) []string {
	return protocol.AccountSecrets(a)
}
