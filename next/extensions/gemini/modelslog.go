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

var modelLogOut io.Writer = os.Stderr

var (
	modelLogMu   sync.Mutex
	modelLogLast = map[string]string{}
)

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

func modelReadOK(accountID string) {
	modelLogMu.Lock()
	delete(modelLogLast, accountID)
	modelLogMu.Unlock()
}

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
