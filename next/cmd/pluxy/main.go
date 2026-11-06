// Command pluxy is the pluxy core: it serves the client APIs and answers through a provider
// extension.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/internal/server"
)

func main() {
	log.SetPrefix("pluxy: ")
	log.SetFlags(0)
	configPath := flag.String("config", "config.json", "path to the config file")
	safeMode := flag.Bool("safe-mode", false, "start no extensions: client requests fail, the management API works")
	watch := flag.Duration("watch-interval", time.Second, "how often to look for config file changes (0: do not watch)")
	flag.Parse()
	if err := run(*configPath, *safeMode, *watch); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(configPath string, safeMode bool, watch time.Duration) error {
	c, err := core.Start(core.Options{ConfigPath: configPath, SafeMode: safeMode})
	if err != nil {
		if !safeMode {
			err = fmt.Errorf("%w (start with -safe-mode to run without extensions)", err)
		}
		return err
	}
	defer c.Stop()
	cfg := c.Config()
	if len(cfg.ClientKeys) == 0 {
		log.Printf("warning: no client_keys set; anyone who can reach %s can use the configured accounts", cfg.Listen)
	}
	if safeMode {
		log.Print("safe mode: no extension runs")
	}
	for _, e := range c.Status().Extensions {
		log.Printf("extension %s %s started", e.ID, e.Version)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(core.ManagementPrefix, c.Management())
	mux.Handle("/", server.NewHandler(c))
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "pluxy: listening on http://%s\n", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if watch > 0 {
		go c.Watch(ctx, watch)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			_ = c.Reload("SIGHUP") // a failure is logged and shown in status
		}
	}()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	srv.Close()
	return nil
}
