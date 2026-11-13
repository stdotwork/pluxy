package host

import (
	"context"

	"github.com/stdotwork/pluxy/next/protocol"
)

// captureMinor is the protocol minor version that adds upstream capture.
const captureMinor = 13

// CaptureFunc receives a captured call's upstream exchanges, with the call's account ID. Every
// string in d has had the call's credentials removed (protocol.Redactor); a body's BodySize is
// the length of what is left of it plus what was cut out (see redactDiagnostic).
type CaptureFunc func(account string, d protocol.Diagnostic)

type captureKey struct{}

// WithCapture returns ctx asking every generate, count_tokens and endpoint call and every session
// turn made with it to capture its upstream exchanges (protocol minor version 13), each handed
// to f as it arrives. Without it a call asks for none.
func WithCapture(ctx context.Context, f CaptureFunc) context.Context {
	return context.WithValue(ctx, captureKey{}, f)
}

// capture returns the CaptureFunc ctx carries for a call to an extension that described itself
// with info, or nil when the call is not to be captured.
func capture(ctx context.Context, info protocol.Info) CaptureFunc {
	f, _ := ctx.Value(captureKey{}).(CaptureFunc)
	if info.ProtocolMinor < captureMinor {
		return nil
	}
	return f
}

// redactDiagnostic removes r's credentials from every string of d. In a body it also removes the
// bytes beside a cut that could be a secret cut in two (Redactor.TrimCut), redacts a start and an
// end with nothing between them as one text and joins them (Redactor.Body), and it sets BodySize
// to the length of what is left of the pieces plus the bytes left out between them, so a body
// that was cut is told from one that only got shorter because a credential was replaced.
func redactDiagnostic(r *protocol.Redactor, d protocol.Diagnostic) protocol.Diagnostic {
	out := protocol.Diagnostic{UpstreamLeftOut: d.UpstreamLeftOut}
	for _, x := range d.Upstream {
		y := protocol.UpstreamExchange{Error: r.String(x.Error), Request: protocol.UpstreamRequest{TimeMs: x.Request.TimeMs,
			Method: r.String(x.Request.Method), URL: r.String(x.Request.URL), Headers: redactHeaders(r, x.Request.Headers),
			HeadersLeftOut: x.Request.HeadersLeftOut, CapturedBody: redactBody(r, x.Request.CapturedBody)}}
		if x.Response != nil {
			y.Response = &protocol.UpstreamResponse{TimeMs: x.Response.TimeMs, Status: x.Response.Status,
				Headers: redactHeaders(r, x.Response.Headers), HeadersLeftOut: x.Response.HeadersLeftOut,
				CapturedBody: redactBody(r, x.Response.CapturedBody)}
		}
		out.Upstream = append(out.Upstream, y)
	}
	return out
}

func redactBody(r *protocol.Redactor, b protocol.CapturedBody) protocol.CapturedBody {
	head, end := b.Body, b.BodyEnd
	left := max(0, b.BodySize-int64(len(head)+len(end))) // what the extension cut out between
	if left > 0 {
		h, e := r.TrimCut(head, end)
		left += int64(len(head) - len(h) + len(end) - len(e))
		head, end = r.String(h), r.String(e)
	} else if end == "" {
		head = r.String(head)
	} else {
		// Nothing lies between the start and the end, so they are one text, as the request
		// log writes them: a secret in one of the forms core removes that is cut between them is
		// removed, but for any part of its start that redacting the start whole had already
		// sent (protocol.BodyRedactor). They are sent on joined: bytes held back from the
		// start and sent with the end could make the end longer than the log keeps of it, which
		// would log a gap that is not there.
		br := r.Body()
		head = br.Write(head) + br.Write(end) + br.Flush()
		end = ""
	}
	return protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head)+len(end)) + left}
}

func redactHeaders(r *protocol.Redactor, h map[string][]string) map[string][]string {
	if h == nil {
		return nil
	}
	out := make(map[string][]string, len(h))
	for k, vs := range h {
		rv := make([]string, len(vs))
		for i, v := range vs {
			rv[i] = r.String(v)
		}
		out[r.String(k)] = rv
	}
	return out
}
