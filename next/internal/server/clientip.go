package server

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// anyForwardHeaders are the headers a proxy may name the client in: the two clientIP reads and
// RFC 7239's Forwarded, which core does not read.
var anyForwardHeaders = []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"}

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
// trusted proxies, the client it names, as today's core reads them. In X-Forwarded-For (every
// line of it, read as one list) that is the last address that is not a trusted proxy, read from
// the right, or the first when all are; else the first line of X-Real-IP. A header with an entry
// that is not an IP address is not used.
func clientIP(trusted []netip.Prefix, r *http.Request) netip.Addr {
	if a, ok := forwardedClient(trusted, r); ok {
		return a
	}
	return remoteAddr(r)
}

// forwardedClient returns the client a trusted proxy names (see clientIP), and false when the
// peer is not a trusted proxy or names no client core can read.
func forwardedClient(trusted []netip.Prefix, r *http.Request) (netip.Addr, bool) {
	peer := remoteAddr(r)
	if !peer.IsValid() || !trustedProxy(trusted, peer) {
		return netip.Addr{}, false
	}
	if vals := r.Header.Values("X-Forwarded-For"); len(vals) > 0 {
		items := strings.Split(strings.Join(vals, ","), ",")
		for i := len(items) - 1; i >= 0; i-- {
			a, ok := parseClient(items[i])
			if !ok {
				break // not an address: the header is not used
			}
			if i == 0 || !trustedProxy(trusted, a) {
				return a, true
			}
		}
	}
	return parseClient(r.Header.Get("X-Real-IP"))
}

// parseClient reads one address from a forwarding header.
func parseClient(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

// remoteAddr returns the address of the connection's peer, or the zero Addr.
func remoteAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// client is where a request came from, as WithClientIP found it.
type client struct {
	addr netip.Addr
	// forwarded: the request names another client in a forwarding header core did not take
	// the client from: the peer is not a trusted proxy, or it is but core cannot read the
	// header (Forwarded, or an X-Forwarded-For that is not a list of addresses). Such a request
	// comes from somewhere else, whatever addr is.
	forwarded bool
}

type clientKey struct{}

// WithClientIP finds each request's client address with the trusted proxies of the config cfg
// returns when the request arrives, for ClientIP in the handlers after it.
func WithClientIP(cfg func() *Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := clientOf(cfg().trusted, r)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), clientKey{}, c)))
	})
}

func clientOf(trusted []netip.Prefix, r *http.Request) client {
	if a, ok := forwardedClient(trusted, r); ok {
		return client{addr: a}
	}
	c := client{addr: remoteAddr(r)}
	for _, h := range anyForwardHeaders {
		if strings.TrimSpace(strings.Join(r.Header.Values(h), "")) != "" {
			c.forwarded = true
		}
	}
	return c
}

func clientFor(r *http.Request) client {
	if c, ok := r.Context().Value(clientKey{}).(client); ok {
		return c
	}
	return clientOf(nil, r)
}

// ClientIP returns where r came from, as WithClientIP found it; without WithClientIP, the
// connection's peer.
func ClientIP(r *http.Request) netip.Addr { return clientFor(r).addr }

// FromThisMachine reports whether r came from a loopback address and was not forwarded from
// elsewhere: a request that names another client in X-Forwarded-For, X-Real-IP or Forwarded,
// where core did not take the client from that header, does not count, so a proxy on this
// machine cannot make remote clients look local.
func FromThisMachine(r *http.Request) bool {
	c := clientFor(r)
	return c.addr.IsLoopback() && !c.forwarded
}
