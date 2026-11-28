package host

import (
	"encoding/json"
	"slices"
	"sync"

	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

type pieces []string

func (e *Extension) accountPieces(a protocol.Account) pieces {
	var settings []string
	if e.opts.SettingsSecrets != nil && len(a.Settings) > 0 {
		settings = e.opts.SettingsSecrets(a.Settings)
	}
	return protocol.AccountSecretsWith(a, settings)
}

func (p pieces) add(q pieces) pieces {
	if len(q) == 0 {
		return p
	}
	seen := make(map[string]struct{}, len(p))
	for _, s := range p {
		seen[s] = struct{}{}
	}
	for _, s := range q {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			p = append(p, s)
		}
	}
	return p
}

func (p pieces) event(ev llm.Event) llm.Event {
	if len(p) == 0 {
		return ev
	}
	if ev.Response != nil && len(ev.Response.Headers) > 0 {
		r := *ev.Response
		r.Headers = p.rateLimitHeaders(r.Headers)
		ev.Response = &r
	}
	if ev.Error == nil {
		return ev
	}
	e := *ev.Error
	e.Message = protocol.RemovePieces(e.Message, p...)
	e.Raw = p.raw(e.Raw)
	e.Headers = p.rateLimitHeaders(e.Headers)
	ev.Error = &e
	return ev
}

func (p pieces) rateLimitHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return h
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		if llm.PlainRateLimitValue(v) {
			out[k] = v
			continue
		}
		out[k] = protocol.RemovePieces(v, p...)
	}
	return out
}

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

type heldCapture struct {
	mu        sync.Mutex
	ds        []protocol.Diagnostic
	succeeded bool
}

func (h *heldCapture) hold(d protocol.Diagnostic) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ds) < 2 {
		h.ds = append(h.ds, d)
		return
	}
	h.ds[1] = joinCaptures(h.ds[1], d)
}

func (h *heldCapture) succeed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.succeeded = true
}

func (h *heldCapture) take() ([]protocol.Diagnostic, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ds := h.ds
	h.ds = nil
	return ds, !h.succeeded
}

func joinCaptures(a, b protocol.Diagnostic) protocol.Diagnostic {
	n := len(a.Upstream) + a.UpstreamLeftOut + len(b.Upstream) + b.UpstreamLeftOut
	all := append(slices.Clip(a.Upstream), b.Upstream...)
	if len(all) > 2 {
		all = all[len(all)-2:]
	}
	return protocol.Diagnostic{Upstream: slices.Clone(all), UpstreamLeftOut: n - len(all)}
}
