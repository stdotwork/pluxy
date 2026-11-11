package sdk

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/stdotwork/pluxy/next/protocol"
)

// Upstream wraps an extension's upstream transport. It delegates to base unchanged,
// preserving its connection pool, HTTP versions and timeout settings. Use the handler's
// context on requests so the transport can access UpstreamInfo and core's cancellation.
// base must be non-nil and must not be changed after use.
func Upstream(base *http.Transport) http.RoundTripper {
	return upstreamTransport{base: base}
}

type upstreamTransport struct {
	base *http.Transport
}

func (t upstreamTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req)
}

// CloseIdleConnections preserves http.Client.CloseIdleConnections through the wrapper.
func (t upstreamTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
}

// Info describes the call making an upstream request. It contains only the account's ID
// and settings, never its credential or the request body. Settings are read-only.
type Info struct {
	AccountID string
	Settings  json.RawMessage
	Model     string
	Format    string
	// Operation is the protocol operation; a session turn uses protocol.TypeSessionTurn.
	Operation string
}

type upstreamInfoKey struct{}

// UpstreamInfo returns the upstream information attached by the SDK dispatcher. It is
// absent on contexts created outside dispatch. Model and Format are empty without a request.
func UpstreamInfo(ctx context.Context) (Info, bool) {
	info, ok := ctx.Value(upstreamInfoKey{}).(Info)
	return info, ok
}

func withUpstreamInfo(ctx context.Context, call protocol.Call) context.Context {
	info := Info{Operation: call.Op}
	if call.Type == protocol.TypeSessionTurn {
		info.Operation = protocol.TypeSessionTurn
	}
	if call.Account != nil {
		info.AccountID, info.Settings = call.Account.ID, call.Account.Settings
	}
	if call.Request != nil {
		info.Model = call.Request.Model
		if call.Request.Native != nil {
			info.Format = call.Request.Native.Format
		}
	}
	return context.WithValue(ctx, upstreamInfoKey{}, info)
}
