package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"sync"

	"github.com/stdotwork/pluxy/next/protocol"
)

// modelLogOut is where a failed live model read is reported; core logs the extension's stderr.
var modelLogOut io.Writer = os.Stderr

var (
	modelLogMu   sync.Mutex
	modelLogLast = map[string]string{} // account ID -> the reason last logged for it
)

// modelReadFailed writes one stderr line for an account's failed live model read, which the
// caller answers with the built-in list. The reason passes through the redactor of the account's
// credential and holds no URL or reply body. A read that fails again for the same reason as its
// last failure is not logged again; modelReadOK ends a streak, so the next failure is.
func modelReadFailed(accountID string, cred *protocol.Credential, reason string) {
	reason = protocol.NewRedactor(cred).String(reason)
	modelLogMu.Lock()
	defer modelLogMu.Unlock()
	if modelLogLast[accountID] == reason {
		return
	}
	modelLogLast[accountID] = reason
	fmt.Fprintf(modelLogOut, "claude: reading the model list failed (%s); serving the built-in list\n", reason)
}

// modelReadOK records that an account's live model read worked.
func modelReadOK(accountID string) {
	modelLogMu.Lock()
	delete(modelLogLast, accountID)
	modelLogMu.Unlock()
}

// requestFailure describes a failed request without its URL, which the HTTP client puts in its errors.
func requestFailure(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return "request failed: " + err.Error()
}
