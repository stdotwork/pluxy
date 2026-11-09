package server

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// forwardHeaders are the headers a trusted proxy names the client in, in the order they are
// read, as today's core reads them.
var forwardHeaders = []string{"X-Forwarded-For", "X-Real-IP"}

// parseTrustedProxies reads trusted_proxies: IP addresses and CIDR ranges.
func parseTrustedProxies(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for i, s := range list {
		s = strings.TrimSpace(s)
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil || a.Zone() != "" {
				return nil, fmt.Errorf("config: trusted_proxies[%d] %q is not an IP address or a CIDR range", i, s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Addr().Is4In6() {
			p = netip.PrefixFrom(p.Addr().Unmap(), max(p.Bits()-96, 0))
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func trustedProxy(trusted []netip.Prefix, a netip.Addr) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP returns where r came from: the connection's peer or, when the peer is one of the
// trusted proxies, the client it names. In X-Forwarded-For (every line of it, read as one list)
// that is the last address that is not a trusted proxy, read from the right, or the first when
// all are; else X-Real-IP. A header with an entry that is not an IP address is not used.
func clientIP(trusted []netip.Prefix, r *http.Request) netip.Addr {
	peer := remoteAddr(r)
	if !peer.IsValid() || !trustedProxy(trusted, peer) {
		return peer
	}
	for _, h := range forwardHeaders {
		vals := r.Header.Values(h)
		if len(vals) == 0 {
			continue
		}
		items := strings.Split(strings.Join(vals, ","), ",")
		for i := len(items) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(items[i]))
			if err != nil || a.Zone() != "" {
				break // not an address: the header is not used
			}
			a = a.Unmap()
			if i == 0 || !trustedProxy(trusted, a) {
				return a
			}
		}
	}
	return peer
}

// remoteAddr returns the address of the connection's peer, or the zero Addr.
func remoteAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

type clientIPKey struct{}

// WithClientIP finds each request's client address with the trusted proxies of the config cfg
// returns when the request arrives, for ClientIP in the handlers after it.
func WithClientIP(cfg func() *Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := clientIP(cfg().trusted, r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientIPKey{}, a)))
	})
}

// ClientIP returns where r came from, as WithClientIP found it; without WithClientIP, the
// connection's peer.
func ClientIP(r *http.Request) netip.Addr {
	if a, ok := r.Context().Value(clientIPKey{}).(netip.Addr); ok {
		return a
	}
	return remoteAddr(r)
}
