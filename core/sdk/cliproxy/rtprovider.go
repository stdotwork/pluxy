package cliproxy

import (
	"net/http"
	"net/url"
	"strings"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// defaultRoundTripperProvider returns a per-auth HTTP RoundTripper based on
// the Auth.ProxyURL value. It caches transports per proxy URL string.
type defaultRoundTripperProvider struct {
	mu    sync.RWMutex
	cache map[string]http.RoundTripper
}

func newDefaultRoundTripperProvider() *defaultRoundTripperProvider {
	return &defaultRoundTripperProvider{cache: make(map[string]http.RoundTripper)}
}

// RoundTripperFor implements coreauth.RoundTripperProvider.
func (p *defaultRoundTripperProvider) RoundTripperFor(auth *coreauth.Auth) http.RoundTripper {
	if auth == nil {
		return nil
	}
	proxy := strings.TrimSpace(auth.ProxyURL)
	if proxy == "" {
		return nil
	}
	p.mu.RLock()
	rt := p.cache[proxy]
	p.mu.RUnlock()
	if rt != nil {
		return rt
	}
	// Build HTTP/HTTPS proxy transport; ignore SOCKS for simplicity here.
	u, err := url.Parse(proxy)
	if err != nil {
		return nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil
	}
	transport := &http.Transport{Proxy: http.ProxyURL(u)}
	p.mu.Lock()
	p.cache[proxy] = transport
	p.mu.Unlock()
	return transport
}
