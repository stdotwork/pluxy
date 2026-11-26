package core

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/stdotwork/pluxy/next/internal/server"
)

// pprofListener serves the profiling pages (the config's pprof section) on an address of their
// own. It serves nothing else, and never the client or management listener. A reload that keeps
// the address keeps the listener running.
type pprofListener struct {
	addr string
	ln   net.Listener
	srv  *http.Server
}

// openPprof binds the profiling listener that cfg asks for, or returns nil when it asks for none
// (the setting is off, or this build has no profiling pages). A listener on the same address as
// prev's is kept. It fails when the address cannot be bound, which refuses the reload.
func openPprof(cfg *server.Config, prev *snapshot) (*pprofListener, error) {
	if !cfg.Pprof.Enable || !pprofBuilt {
		return nil, nil
	}
	if prev != nil && prev.pprof != nil && prev.pprof.addr == cfg.Pprof.Addr {
		return prev.pprof, nil
	}
	ln, err := net.Listen("tcp", cfg.Pprof.Addr)
	if err != nil {
		return nil, fmt.Errorf("config: pprof.addr %q: %w", cfg.Pprof.Addr, err)
	}
	return &pprofListener{
		addr: cfg.Pprof.Addr,
		ln:   ln,
		srv:  &http.Server{Handler: pprofHandler(), ReadHeaderTimeout: 30 * time.Second},
	}, nil
}

// serve starts answering on the listener and logs the address it answers on.
func (p *pprofListener) serve() {
	log.Printf("pprof: listening on http://%s/debug/pprof/", p.ln.Addr())
	go func() {
		if err := p.srv.Serve(p.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pprof: %v", err)
		}
	}()
}

// close stops the listener, and any profile still being served.
func (p *pprofListener) close() {
	_ = p.srv.Close()
	_ = p.ln.Close() // the listener of a server that never served is closed here
}

// swapPprof makes next's profiling listener the live one: it closes prev's when next does not keep
// it, and starts next's when it is new. It runs when next is published.
func swapPprof(next, prev *snapshot) {
	var old *pprofListener
	if prev != nil {
		old = prev.pprof
	}
	if old != nil && old != next.pprof {
		old.close()
	}
	if next.pprof != nil && next.pprof != old {
		next.pprof.serve()
	}
	if next.Config.Pprof.Enable && !pprofBuilt {
		log.Print("pprof: this build has no profiling pages, so the pprof setting does nothing; to get them, build with: go build -tags pprof ./cmd/pluxy")
	}
}
