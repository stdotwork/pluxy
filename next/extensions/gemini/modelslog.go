package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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
// caller answers with the built-in list. The reason is fixed text, never text of an error or a
// reply, and still passes through the redactor of the account's credential. A read that fails
// again for the same reason as its last failure is not logged again, whatever the credential;
// modelReadOK ends a streak, so the next failure is logged.
func modelReadFailed(accountID string, cred *protocol.Credential, reason string) {
	reason = protocol.NewRedactor(cred).String(reason)
	modelLogMu.Lock()
	defer modelLogMu.Unlock()
	if modelLogLast[accountID] == reason {
		return
	}
	modelLogLast[accountID] = reason
	fmt.Fprintf(modelLogOut, "gemini: reading the model list failed (%s); serving the built-in list\n", reason)
}

// modelReadOK records that an account's live model read worked.
func modelReadOK(accountID string) {
	modelLogMu.Lock()
	delete(modelLogLast, accountID)
	modelLogMu.Unlock()
}

// requestFailure names why a request failed in fixed words. Text of the error is never logged: the
// HTTP client quotes bytes of the reply in it, and a reply can hold an account's email or ID.
func requestFailure(err error) string {
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the request timed out"
	case errors.As(err, &dns):
		return "the host name could not be resolved"
	case errors.As(err, &op):
		return "the connection failed"
	}
	return "the request failed"
}
