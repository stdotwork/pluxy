package host

import (
	"fmt"
	"net/http"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// aliveMinor is the protocol minor version that adds alive frames: an extension that reports it
// is told it may send them (protocol.Call.Alive), and core ends a call or turn on which it has
// received nothing at all for longer than the watchdog allows.
const aliveMinor = 19

// watchdogGrace is what the watchdog adds to the account's limit: the SDK sends an alive frame
// at most every 10 seconds while upstream bytes arrive, and ends the upstream request itself
// once the limit passes, so a call still silent a minute after that has stopped answering.
// Tests shorten it.
var watchdogGrace = time.Minute

// watchdog returns how long core waits for the next frame of a call or turn whose account has the
// resolved timeouts t (resolveTimeouts): the idle limit plus watchdogGrace for a streamed request,
// and the longer of the idle and total limits plus watchdogGrace for one that is not (its
// upstream request may be streamed or not). 0, no watchdog, when a limit it needs is off.
func watchdog(t *protocol.Timeouts, stream bool) time.Duration {
	if t == nil || t.IdleMS == nil || *t.IdleMS <= 0 {
		return 0
	}
	limit := time.Duration(*t.IdleMS) * time.Millisecond
	if !stream {
		if t.TotalMS == nil || *t.TotalMS <= 0 {
			return 0
		}
		limit = max(limit, time.Duration(*t.TotalMS)*time.Millisecond)
	}
	return limit + watchdogGrace
}

// timedOut logs and returns the error of a call or turn on account that the watchdog ended
// after limit. It is retry-safe before the reply started, as an upstream timeout is then.
func (e *Extension) timedOut(account string, limit time.Duration, started bool) *llm.Error {
	e.out.core(fmt.Sprintf("extension_timeout: the extension sent nothing for %s on account %q; the attempt is ended", limit, account))
	return &llm.Error{Code: llm.CodeExtensionTimeout, Status: http.StatusGatewayTimeout, Scope: llm.ScopeEndpoint, Retryable: !started,
		Message: fmt.Sprintf("extension %s sent nothing for %s", e.manifest.ID, limit)}
}
