package host

import (
	"encoding/json"
	"slices"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

// pieces are the secrets of the account a call runs on (protocol.AccountSecrets), whose pieces
// an upstream may echo in its error: a key it refuses, cut to its first characters, or a token
// in part. The call's Redactor removes only whole secrets and their parts, so core also removes
// every run of text that holds 4 or more characters of one of these (protocol.RemovePieces) from
// the upstream's error text: an error event's message and raw data, and the body of an upstream
// reply with an error status in the call's capture. A reply that succeeded is kept as it is: the
// rule would cut words of a model's output that share 4 characters with a secret.
type pieces []string

// accountPieces returns the pieces of account a, after outbound has resolved its proxy.
func accountPieces(a protocol.Account) pieces {
	return protocol.AccountSecrets(a)
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

// raw returns the JSON b with p's pieces removed from each of its strings, object keys included;
// a string that changes is written again, every other byte is kept.
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
		if json.Unmarshal(b[i:end+1], &s) == nil {
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

// diagnostic returns d with p's pieces removed from the body of each upstream reply with an error
// status (400 and up), and that body's size made smaller by what they took away.
func (p pieces) diagnostic(d protocol.Diagnostic) protocol.Diagnostic {
	if len(p) == 0 {
		return d
	}
	out := protocol.Diagnostic{UpstreamLeftOut: d.UpstreamLeftOut, Upstream: slices.Clone(d.Upstream)}
	for i, x := range out.Upstream {
		if x.Response == nil || x.Response.Status < 400 {
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
