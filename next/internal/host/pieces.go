package host

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// pieces are the secrets of the account a call runs on (protocol.AccountSecretsWith), whose
// pieces an upstream may echo in its error: a key it refuses, cut to its first characters, or a
// token in part. The call's Redactor removes only whole secrets and their parts, so core also
// removes every run of text that holds 4 or more characters of one of these, or 8 of a PEM
// private key's body (protocol.RemovePieces), from the upstream's error
// text: an error event's message and the string values of its raw data, and in the call's capture
// the body of an upstream reply with an error status, or of every reply when the call ended in an
// error. A call that succeeded keeps its replies as they are: the rule would cut words of a
// model's output that share 4 characters with a secret.
type pieces []string

// accountPieces returns the pieces of account a, after outbound has resolved its proxy: its
// credential, secret headers and proxy, and the secrets core's own rules find in its settings
// (Options.SettingsSecrets, as for its Redactor).
func (e *Extension) accountPieces(a protocol.Account) pieces {
	var settings []string
	if e.opts.SettingsSecrets != nil && len(a.Settings) > 0 {
		settings = e.opts.SettingsSecrets(a.Settings)
	}
	return protocol.AccountSecretsWith(a, settings)
}

// add returns p with the secrets of q it does not hold yet.
func (p pieces) add(q pieces) pieces {
	for _, s := range q {
		if !slices.Contains(p, s) {
			p = append(p, s)
		}
	}
	return p
}

// event returns ev with p's pieces removed from its error's message and raw data, if it is an
// error.
func (p pieces) event(ev llm.Event) llm.Event {
	if len(p) == 0 || ev.Error == nil {
		return ev
	}
	e := *ev.Error
	e.Message = protocol.RemovePieces(e.Message, p...)
	e.Raw = p.raw(e.Raw)
	ev.Error = &e
	return ev
}

// raw returns the JSON b with p's pieces removed from each of its string values; object keys are
// names, not upstream text, and are kept. A string that changes is written again, every other
// byte is kept.
func (p pieces) raw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return b
	}
	if !json.Valid(b) {
		if s := protocol.RemovePieces(string(b), p...); s != string(b) {
			return llm.RawJSON([]byte(s))
		}
		return b
	}
	var out []byte
	last := 0
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		end := i + 1
		for b[end] != '"' {
			if b[end] == '\\' {
				end++
			}
			end++
		}
		var s string
		if !objectKey(b, end+1) && json.Unmarshal(b[i:end+1], &s) == nil {
			if c := protocol.RemovePieces(s, p...); c != s {
				enc, _ := json.Marshal(c)
				out = append(append(out, b[last:i]...), enc...)
				last = end + 1
			}
		}
		i = end
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// objectKey reports whether the JSON string token that ends before b[i] is an object key: the
// next byte but white space is a colon.
func objectKey(b []byte, i int) bool {
	for ; i < len(b); i++ {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case ':':
			return true
		}
		return false
	}
	return false
}

// diagnostic returns d with p's pieces removed from the body of each upstream reply with an error
// status (400 and up), or of every reply when failed (the call ended in an error, such as a 200
// stream that ends in an error event), and that body's size made smaller by what they took away.
func (p pieces) diagnostic(d protocol.Diagnostic, failed bool) protocol.Diagnostic {
	if len(p) == 0 {
		return d
	}
	out := protocol.Diagnostic{UpstreamLeftOut: d.UpstreamLeftOut, Upstream: slices.Clone(d.Upstream)}
	for i, x := range out.Upstream {
		if x.Response == nil || !failed && x.Response.Status < 400 {
			continue
		}
		r := *x.Response
		b := r.CapturedBody
		head, end := protocol.RemovePieces(b.Body, p...), protocol.RemovePieces(b.BodyEnd, p...)
		b.BodySize -= int64(len(b.Body) + len(b.BodyEnd) - len(head) - len(end))
		b.Body, b.BodyEnd = head, end
		r.CapturedBody = b
		out.Upstream[i].Response = &r
	}
	return out
}

// heldCapture holds a call's captures, already redacted, until the call ends. An extension sends
// its diagnostic frame before the call's terminal event, and only that event says whether the
// call failed, so whether the replies in the capture lose the pieces of the account's secrets.
// It holds at most two captures, the first and the last, as the request log keeps a request's
// first and last exchange and counts the others (see joinCaptures): an extension that sends more
// diagnostic frames than the protocol allows costs no more memory, and is not refused.
//
// Its mutex also guards whether the call succeeded, which Next records and Close reads, perhaps
// on another goroutine.
type heldCapture struct {
	mu        sync.Mutex
	ds        []protocol.Diagnostic
	succeeded bool
}

// hold keeps d, joined to the last capture held when there are two already.
func (h *heldCapture) hold(d protocol.Diagnostic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ds) < 2 {
		h.ds = append(h.ds, d)
		return
	}
	h.ds[1] = joinCaptures(h.ds[1], d)
}

// succeed records that the call ended with a finish event.
func (h *heldCapture) succeed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.succeeded = true
}

// take returns the captures held, once, and whether the call failed: it ended in an error, or
// was closed before its terminal event.
func (h *heldCapture) take() ([]protocol.Diagnostic, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ds := h.ds
	h.ds = nil
	return ds, !h.succeeded
}

// joinCaptures returns the exchanges of a and then b as one capture for the request log, which
// logs only a request's first exchange and its last and counts the ones between: b's last
// exchange stays last, the one before it stands in for the others, and UpstreamLeftOut counts
// the rest, so the log's count stays right. It holds at most two exchanges.
func joinCaptures(a, b protocol.Diagnostic) protocol.Diagnostic {
	n := len(a.Upstream) + a.UpstreamLeftOut + len(b.Upstream) + b.UpstreamLeftOut
	all := append(slices.Clip(a.Upstream), b.Upstream...)
	if len(all) > 2 {
		all = all[len(all)-2:]
	}
	return protocol.Diagnostic{Upstream: slices.Clone(all), UpstreamLeftOut: n - len(all)}
}
