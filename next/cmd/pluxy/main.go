// Command pluxy is the pluxy core: it serves OpenAI Chat Completions and answers through a
// provider extension.
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
	"slices"
	"syscall"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func main() {
	log.SetPrefix("pluxy: ")
	log.SetFlags(0)
	configPath := flag.String("config", "config.json", "path to the config file")
	flag.Parse()
	if err := run(*configPath); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := server.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if len(cfg.ClientKeys) == 0 {
		log.Printf("warning: no client_keys set; anyone who can reach %s can use the configured accounts", cfg.Listen)
	}
	ext, err := host.Start(cfg.Extension.Manifest, host.Options{})
	if err != nil {
		return err
	}
	defer ext.Stop()
	info := ext.Info()
	log.Printf("extension %s %s started (protocol %d)", info.ID, info.Version, info.Protocol)
	go func() {
		<-ext.Exited()
		log.Printf("extension %s exited; requests will fail until core restarts", info.ID)
	}()

	store, err := cred.Open(cred.FileBackend{Dir: cfg.CredentialsDir})
	if err != nil {
		return err
	}
	creds := cred.NewManager(store, provider{ext}, cred.Options{Reserved: func(id string) bool {
		return slices.ContainsFunc(cfg.Extension.Accounts, func(a server.Account) bool { return a.ID == id })
	}})
	defer creds.Close()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	handler, err := server.New(cfg, func(ctx context.Context, a protocol.Account, req llm.Request) server.Stream {
		return ext.Generate(ctx, a, req)
	}, creds)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "pluxy: listening on http://%s\n", ln.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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

// provider adapts the extension to the credential manager.
type provider struct{ *host.Extension }

func (p provider) Login(ctx context.Context, account protocol.Account, login protocol.Login) cred.LoginCall {
	return p.Extension.Login(ctx, account, login)
}
