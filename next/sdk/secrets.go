package sdk

import "github.com/stdotwork/pluxy/next/protocol"

func AccountSecrets(a protocol.Account) []string {
	return protocol.AccountSecrets(a)
}
