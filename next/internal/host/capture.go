package host

import (
	"context"

	"github.com/stdotwork/pluxy/next/protocol"
)

// captureMinor is the protocol minor version that adds upstream capture.
const captureMinor = 13

// CaptureFunc receives a captured call's upstream exchanges, with the call's account ID. Every
// string in d has had the call's credentials removed in the spellings protocol.Redactor knows,
// and the bytes beside a cut or a stopped read that start a form of a credential, of 4 bytes or
// more, are left out. A start of a secret spelled another way (JSON or percent escapes, a
// carriage return before a line break) can still be where a body was cut or its read stopped: the
// caller that writes a body trims what else could be one, as the request log does (the window
// and run of key characters in server/upstreamlog.go). A body's BodySize is the length of what is
// left of it plus what was cut out (see redactDiagnostic).
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
// bytes beside a cut that could be a secret cut in two (Redactor.TrimCut), and, where the body was
// not read to its end (BodyStopped), the run at its end that starts a form of a credential
// (Redactor.TrimCut again, so a start spelled with escapes is not recognised: see CaptureFunc);
// it redacts a start and an end with nothing between them as one text and joins them
// (Redactor.Body), and it sets BodySize to the length of what is left of the pieces plus the bytes
// left out between them, so a body that was cut is told from one that only got shorter because a
// credential was replaced. The bytes removed where the read stopped are not counted: nothing
// after them is either.
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
	if b.BodyStopped {
		head, end = trimStop(r, head, end, left > 0)
	}
	if left > 0 {
		h, e := r.TrimCut(head, end)
		left += int64(len(head) - len(h) + len(end) - len(e))
		head, end = r.String(h), r.String(e)
	} else if end == "" {
		head = r.String(head)
	} else {
		// Nothing lies between the start and the end, so they are one text, as the request
		// log writes them: a secret in one of the forms core removes, or spelled with other
		// escapes core decodes, that is cut between them is removed, but for the part of its
		// start that redacting the start whole had already sent as it was; an escape the start
		// ends with that the end completes is replaced too (protocol.BodyRedactor). They
		// are sent on joined: bytes held back from the start and sent with the end could make
		// the end longer than the log keeps of it, which would log a gap that is not there.
		br := r.Body()
		head = br.Write(head) + br.Write(end) + br.Flush()
		end = ""
	}
	return protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head)+len(end)) + left, BodyStopped: b.BodyStopped}
}

// trimStop returns head and end, the pieces of a body whose read stopped, without the run at the
// body's end that is the start of a form of r's credential, of at least 4 bytes (TrimCut, on one
// side): the redaction cannot recognise it. A start and an end with nothing between them (gap
// false) are one text, so the run can reach back into the start.
func trimStop(r *protocol.Redactor, head, end string, gap bool) (string, string) {
	if gap && end != "" {
		end, _ = r.TrimCut(end, "")
		return head, end
	}
	text, _ := r.TrimCut(head+end, "")
	if len(text) >= len(head) {
		return head, text[len(head):]
	}
	return text, ""
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
