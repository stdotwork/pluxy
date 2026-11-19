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
// once its limit passes, so a call still silent a minute after that has stopped answering.
// Tests shorten it.
var watchdogGrace = time.Minute

// watchdog returns how long core waits for the next frame of a call or turn whose account has the
// resolved timeouts t (resolveTimeouts): as long as the SDK may still wait on the upstream, plus
// watchdogGrace. Nothing reaches core before the upstream's response headers, so a streamed
// request may be silent for the header limit or the idle limit, whichever is longer, and a request
// that is not streamed (its upstream request may be either) for the longest of the header, idle and
// total limits. 0, no watchdog, when a limit it needs is off or unset: the SDK then waits without
// end, and core cannot tell a quiet call from a stuck one.
func watchdog(t *protocol.Timeouts, stream bool) time.Duration {
	if t == nil {
		return 0
	}
	limits := []*int64{t.HeaderMS, t.IdleMS}
	if !stream {
		limits = append(limits, t.TotalMS)
	}
	var longest time.Duration
	for _, ms := range limits {
		if ms == nil || *ms <= 0 {
			return 0
		}
		longest = max(longest, time.Duration(*ms)*time.Millisecond)
	}
	return longest + watchdogGrace
}

// timedOut logs and returns the error of a call or turn on account that the watchdog ended
// after limit. It is retry-safe before the reply started, as an upstream timeout is then.
func (e *Extension) timedOut(account string, limit time.Duration, started bool) *llm.Error {
	e.out.core(fmt.Sprintf("extension_timeout: the extension sent nothing for %s on account %q; the attempt is ended", limit, account))
	return &llm.Error{Code: llm.CodeExtensionTimeout, Status: http.StatusGatewayTimeout, Scope: llm.ScopeEndpoint, Retryable: !started,
		Message: fmt.Sprintf("extension %s sent nothing for %s", e.manifest.ID, limit)}
}
