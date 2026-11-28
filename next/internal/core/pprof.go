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

type pprofListener struct {
	addr string
	ln   net.Listener
	srv  *http.Server
}

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

func (p *pprofListener) serve() {
	log.Printf("pprof: listening on http://%s/debug/pprof/", p.ln.Addr())
	go func() {
		if err := p.srv.Serve(p.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("pprof: %v", err)
		}
	}()
}

func (p *pprofListener) close() {
	_ = p.srv.Close()
	_ = p.ln.Close()
}

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
