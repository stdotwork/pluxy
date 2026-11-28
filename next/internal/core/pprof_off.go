//go:build !pprof

package core

import "net/http"

const pprofBuilt = false

func pprofHandler() http.Handler { return nil }
