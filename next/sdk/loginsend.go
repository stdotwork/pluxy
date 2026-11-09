package sdk

import (
	"context"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
)

// TrackSend lets an extension tell, after an HTTP request failed, whether the request may have
// reached the server. It returns a copy of req and a function. Send the copy; after Do returns
// an error, call the function: false means nothing of the request was written to the connection,
// so the provider cannot have seen it and sending it again is safe. This is the test a login or
// refresh call uses to decide whether its error is retryable (protocol README, "Errors in login
// and refresh calls"). req itself is not changed.
//
// A request counts as sent as soon as writing its headers began. That is on purpose: it errs
// toward "sent", which makes the error not retryable. HTTP/2 encodes the headers before it writes
// them to the connection, and a header field may sit in a buffer when the connection breaks, so
// the function can say true for a request that never left this machine, but never false for one
// that did. A connection refused, a failed DNS lookup, a failed TLS handshake and a timeout
// while connecting all happen before any header is written, so they report false.
//
// A trace that is already on req's context keeps working: both traces are called.
func TrackSend(req *http.Request) (*http.Request, func() bool) {
	ctx, sent := TrackSendContext(req.Context())
	return req.WithContext(ctx), sent
}

// TrackSendContext does what TrackSend does for every request made with the returned context. Use
// it when the request is made by a function you call but do not own. The function it returns
// reports whether any of those requests may have been written.
func TrackSendContext(ctx context.Context) (context.Context, func() bool) {
	var sent atomic.Bool
	mark := func() { sent.Store(true) }
	trace := &httptrace.ClientTrace{
		WroteHeaderField: func(string, []string) { mark() },
		WroteHeaders:     mark,
		WroteRequest:     func(httptrace.WroteRequestInfo) { mark() },
	}
	return httptrace.WithClientTrace(ctx, trace), sent.Load
}
