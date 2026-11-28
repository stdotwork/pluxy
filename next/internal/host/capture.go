package host

import (
	"context"

	"github.com/stdotwork/pluxy/next/protocol"
)

const captureMinor = 13

type CaptureFunc func(account string, d protocol.Diagnostic)

type captureKey struct{}

func WithCapture(ctx context.Context, f CaptureFunc) context.Context {
	return context.WithValue(ctx, captureKey{}, f)
}

func capture(ctx context.Context, info protocol.Info) CaptureFunc {
	f, _ := ctx.Value(captureKey{}).(CaptureFunc)
	if info.ProtocolMinor < captureMinor {
		return nil
	}
	return f
}

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
	left := max(0, b.BodySize-int64(len(head)+len(end)))
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
		br := r.Body()
		head = br.Write(head) + br.Write(end) + br.Flush()
		end = ""
	}
	return protocol.CapturedBody{Body: head, BodyEnd: end, BodySize: int64(len(head)+len(end)) + left, BodyStopped: b.BodyStopped}
}

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
