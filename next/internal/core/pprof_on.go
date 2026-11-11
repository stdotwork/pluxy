//go:build pprof

package core

import (
	"net/http"
	"net/http/pprof"
)

// pprofBuilt is true in a build with the pprof tag: it has the profiling pages.
const pprofBuilt = true

// pprofHandler returns the profiling pages on a mux of their own. Importing net/http/pprof also
// registers them on http.DefaultServeMux; core serves no request through that mux, so they are
// not reachable there.
func pprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}
