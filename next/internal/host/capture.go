package host

import (
	"context"

	"github.com/stdotwork/pluxy/next/protocol"
)

// captureMinor is the protocol minor version that adds upstream capture.
const captureMinor = 13

// CaptureFunc receives a captured call's upstream exchanges, with the call's account ID. Every
// string in d has had the call's credentials removed (protocol.Redactor).
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

// redactDiagnostic removes r's credentials from every string of d.
func redactDiagnostic(r *protocol.Redactor, d protocol.Diagnostic) protocol.Diagnostic {
	out := protocol.Diagnostic{UpstreamLeftOut: d.UpstreamLeftOut}
	body := func(b protocol.CapturedBody) protocol.CapturedBody {
		return protocol.CapturedBody{Body: r.String(b.Body), BodyEnd: r.String(b.BodyEnd), BodySize: b.BodySize}
	}
	for _, x := range d.Upstream {
		y := protocol.UpstreamExchange{Error: r.String(x.Error), Request: protocol.UpstreamRequest{TimeMs: x.Request.TimeMs,
			Method: r.String(x.Request.Method), URL: r.String(x.Request.URL), Headers: redactHeaders(r, x.Request.Headers),
			CapturedBody: body(x.Request.CapturedBody)}}
		if x.Response != nil {
			y.Response = &protocol.UpstreamResponse{TimeMs: x.Response.TimeMs, Status: x.Response.Status,
				Headers: redactHeaders(r, x.Response.Headers), CapturedBody: body(x.Response.CapturedBody)}
		}
		out.Upstream = append(out.Upstream, y)
	}
	return out
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
