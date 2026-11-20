package server

import (
	"net/http"
	"testing"
)

func TestRequestContentEncoding(t *testing.T) {
	for _, c := range []struct {
		name, header, want string
		ok                 bool
	}{
		{"gzip", "gzip", "gzip", true},
		{"identity", "identity", "", true},
		{"gzip twice", "gzip, gzip", "gzip, gzip", false},
		{"brotli", "br", "br", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			h.Set("Content-Encoding", c.header)
			got, ok := requestContentEncoding(h)
			if got != c.want || ok != c.ok {
				t.Errorf("requestContentEncoding(%q) = %q, %v; want %q, %v", c.header, got, ok, c.want, c.ok)
			}
		})
	}
}
