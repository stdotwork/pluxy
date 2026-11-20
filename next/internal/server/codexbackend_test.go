package server

import (
	"net/http"
	"strings"
	"testing"
)

// The Codex CLI's backend calls hold to the route that matched: a segment that is empty, a dot
// segment, or one that unescapes to a slash, backslash or control character is refused.
func TestSafeBackendPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/wham/usage": true,
		"/wham/tasks/task_e_123/turns/t1/sibling_turns": true,
		"/wham/tasks/a%20b":                             true,
		"/wham/environments/by-repo/github/o/r":         true,
		"":                                              false,
		"wham/usage":                                    false,
		"/":                                             false,
		"/wham//usage":                                  false,
		"/wham/usage/":                                  false,
		"/wham/./usage":                                 false,
		"/wham/../codex/responses":                      false,
		"/wham/%2e%2e/x":                                false,
		"/wham/%2E/x":                                   false,
		"/wham/a%2Fb":                                   false,
		"/wham/a%5Cb":                                   false,
		"/wham/a%00b":                                   false,
		"/wham/a%0ab":                                   false,
		"/wham/%zz":                                     false,
	} {
		if got := safeBackendPath(path); got != want {
			t.Errorf("safeBackendPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// Each listed call is a method and a path below /backend-api that no other route serves, and none
// takes a wildcard that runs on past a segment.
func TestCodexBackendRoutes(t *testing.T) {
	seen := map[string]bool{}
	for _, route := range codexBackendRoutes {
		method, path, ok := strings.Cut(route, " ")
		if !ok || !strings.HasPrefix(path, "/") || strings.Contains(path, "...") || strings.Contains(path, "$") {
			t.Errorf("route %q is not a method and a path", route)
		}
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut:
		default:
			t.Errorf("route %q has the method %q", route, method)
		}
		if seen[route] {
			t.Errorf("route %q is listed twice", route)
		}
		seen[route] = true
		for served := range endpointRoutes {
			if codexBackendRoot+path == served {
				t.Errorf("route %q is also an endpoint route", route)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no routes")
	}
}
