package server

import (
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"slices"
	"strings"
)

type CORSConfig struct {
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
}

var corsAllowHeaders = []string{"Authorization", "Content-Type", "X-Api-Key", "X-Goog-Api-Key", "Anthropic-Version", "Anthropic-Beta"}

const corsExposeHeaders = TraceHeader + ", Retry-After"

const corsMethods = "GET, POST, OPTIONS"

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

func (c *CORSConfig) allow(origin string) string {
	if slices.Contains(c.AllowedOrigins, "*") {
		return "*"
	}
	if origin != "" && slices.Contains(c.AllowedOrigins, strings.ToLower(origin)) {
		return origin
	}
	return ""
}

func (c *CORSConfig) perOrigin() bool {
	return len(c.AllowedOrigins) > 0 && !slices.Contains(c.AllowedOrigins, "*")
}

// browsers send no credentials with a preflight.
func withCORS(src Source, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g, release := src.Acquire()
		allow := g.Config.CORS.allow(r.Header.Get("Origin"))
		perOrigin := g.Config.CORS.perOrigin()
		release()
		h := w.Header()
		if perOrigin {
			// a shared cache must not give one origin's reply to another.
			h.Add("Vary", "Origin")
		}
		if allow == "" {
			next.ServeHTTP(w, r)
			return
		}
		h.Set("Access-Control-Allow-Origin", allow)
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
