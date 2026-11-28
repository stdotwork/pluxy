package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// RFC 7239's Forwarded, which core does not read.
var anyForwardHeaders = []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"}

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
				break
			}
			if i == 0 || !trustedProxy(trusted, a) {
				return a, true
			}
		}
	}
	return parseClient(r.Header.Get("X-Real-IP"))
}

func parseClient(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func remoteAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// http.LocalAddrContextKey, which net/http sets for each connection, HTTP/2 over TLS included
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

type client struct {
	addr      netip.Addr
	forwarded bool
	// addr is the connection's peer and the same address the connection arrived at. Only a
	// program on this machine can open such a connection: the other end of a TCP handshake sends
	// its replies to the source address, which here is this machine's own.
	ownAddress bool
}

type clientKey struct{}

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

func ClientIP(r *http.Request) netip.Addr { return clientFor(r).addr }

func FromThisMachine(r *http.Request) bool {
	c := clientFor(r)
	return (c.addr.IsLoopback() || c.ownAddress) && !c.forwarded
}
