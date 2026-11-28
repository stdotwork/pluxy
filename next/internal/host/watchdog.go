package host

import (
	"fmt"
	"net/http"
	"time"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

const AliveMinor = 23

var watchdogGrace = time.Minute

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

func (e *Extension) timedOut(account string, limit time.Duration, started bool) *llm.Error {
	e.out.core(fmt.Sprintf("extension_timeout: the extension sent nothing for %s on account %q; the attempt is ended", limit, account))
	return &llm.Error{Code: llm.CodeExtensionTimeout, Status: http.StatusGatewayTimeout, Scope: llm.ScopeEndpoint, Retryable: !started,
		Message: fmt.Sprintf("extension %s sent nothing for %s", e.manifest.ID, limit)}
}
