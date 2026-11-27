package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// anyForwardHeaders are the headers a proxy may name the client in: the two core reads the client
// from (X-Forwarded-For and X-Real-IP, see forwardedClient) and RFC 7239's Forwarded, which core
// does not read.
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

// forwardedClient returns where r came from when its peer is one of the trusted proxies: the
// client it names, as today's core reads it. In X-Forwarded-For (every line of it, read as one
// list) that is the last address that is not a trusted proxy, read from the right, or the first
// when all are; else the first line of X-Real-IP. A header with an entry that is not an IP address
// is not used. It returns false when the peer is not a trusted proxy or names no client core can
// read; clientOf then takes the peer.
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

// localAddr returns the address the request's connection arrived at (http.LocalAddrContextKey,
// which net/http sets for each connection, HTTP/2 over TLS included), or the zero Addr when it is
// not known or not an IP address.
func localAddr(r *http.Request) netip.Addr {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || a == nil {
		return netip.Addr{}
	}
	ap, err := netip.ParseAddrPort(a.String())
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
	// ownAddress: addr is the connection's peer and the same address the connection arrived
	// at. Only a program on this machine can open such a connection: the other end of a TCP
	// handshake sends its replies to the source address, which here is this machine's own.
	// It is false when addr is a client a trusted proxy named, since that is the proxy's word.
	ownAddress bool
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
	if local := localAddr(r); c.addr.IsValid() && local == c.addr {
		c.ownAddress = true
	}
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

// FromThisMachine reports whether r came from this machine and was not forwarded from
// elsewhere. A request comes from this machine when it came from a loopback address, or from
// the very address its connection arrived at (core listening on a network address of this
// machine, called by a program here at that address). Another address of this machine does not
// count: only the address the call arrived at shows the caller is here. A request that names
// another client in X-Forwarded-For, X-Real-IP or Forwarded, where core did not take the client
// from that header, does not count, so a proxy on this machine cannot make remote clients look
// local.
func FromThisMachine(r *http.Request) bool {
	c := clientFor(r)
	return (c.addr.IsLoopback() || c.ownAddress) && !c.forwarded
}
