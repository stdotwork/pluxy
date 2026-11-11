//go:build !pprof

package core

import "net/http"

// pprofBuilt is false in the default build, which has no profiling pages and does not link
// net/http/pprof. The pprof setting is still checked, and core says in its log that it does
// nothing.
const pprofBuilt = false

// pprofHandler is never called in this build: openPprof returns before it when pprofBuilt is false.
func pprofHandler() http.Handler { return nil }
