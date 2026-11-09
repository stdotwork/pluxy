package server

import (
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
)

// CORSConfig says which web pages may call the client API from a browser (CORS). Empty: none,
// and core sends no CORS headers. Today's core allowed every origin on every route; this core
// allows only the origins listed here, and only on the client API (the panel and the management
// API stay same-site).
type CORSConfig struct {
	// AllowedOrigins are the origins allowed, each scheme://host[:port], such as
	// https://app.example.com; "*" allows every origin, as today's core does.
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
}

// corsAllowHeaders are the request headers a page may always send: the ones client keys and
// the client formats use. A preflight's own list is allowed too.
var corsAllowHeaders = []string{"Authorization", "Content-Type", "X-Api-Key", "X-Goog-Api-Key", "Anthropic-Version", "Anthropic-Beta"}

// corsExposeHeaders are the reply headers a page may read: the ones core adds.
const corsExposeHeaders = TraceHeader + ", Retry-After"

// corsMethods are the methods the client API serves.
const corsMethods = "GET, POST, OPTIONS"

// prepare checks the origins and writes each in the form browsers send it.
func (c *CORSConfig) prepare() error {
	for i, o := range c.AllowedOrigins {
		if o == "*" {
			continue
		}
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("config: cors.allowed_origins[%d] %q must be \"*\" or an origin: http:// or https://, a host and maybe a port, nothing else", i, o)
		}
		c.AllowedOrigins[i] = u.Scheme + "://" + strings.ToLower(u.Host)
	}
	return nil
}

// allow returns the Access-Control-Allow-Origin value for a request from origin: "*" when every
// origin is allowed, origin itself when it is listed, "" when it may not call the API.
func (c *CORSConfig) allow(origin string) string {
	if slices.Contains(c.AllowedOrigins, "*") {
		return "*"
	}
	if origin != "" && slices.Contains(c.AllowedOrigins, strings.ToLower(origin)) {
		return origin
	}
	return ""
}

// withCORS answers CORS for the client API: it adds the CORS headers to replies to allowed
// origins and answers their preflight requests itself (204, before the client key is checked,
// since browsers send no credentials with a preflight). Requests from other origins, and every
// request when no origin is allowed, go on unchanged.
func withCORS(src Source, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, release := src.Acquire()
		allow := g.Config.CORS.allow(r.Header.Get("Origin"))
		release()
		if allow == "" {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", allow)
		if allow != "*" {
			h.Add("Vary", "Origin")
		}
		h.Set("Access-Control-Allow-Methods", corsMethods)
		h.Set("Access-Control-Allow-Headers", allowHeaders(r.Header.Values("Access-Control-Request-Headers")))
		h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowHeaders returns corsAllowHeaders and the headers a preflight asks for, each once.
func allowHeaders(requested []string) string {
	out := slices.Clone(corsAllowHeaders)
	for _, v := range requested {
		for _, name := range strings.Split(v, ",") {
			name = textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
			if name != "" && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return strings.Join(out, ", ")
}
