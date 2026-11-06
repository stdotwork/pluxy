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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/internal/pages"
	"github.com/stdotwork/pluxy/next/internal/panel"
	"github.com/stdotwork/pluxy/next/internal/server"
)

func main() {
	log.SetPrefix("pluxy: ")
	log.SetFlags(0)
	configPath := flag.String("config", "config.json", "path to the config file")
	safeMode := flag.Bool("safe-mode", false, "start no extensions: client requests fail, the management API works")
	watch := flag.Duration("watch-interval", time.Second, "how often to look for config file changes (0: do not watch)")
	bundled := flag.String("bundled", "", "index of the registry of bundled packages (default: bundled/index.json beside this program, when it exists)")
	flag.Parse()
	if err := run(*configPath, *safeMode, *watch, *bundled); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// defaultBundled returns bundled/index.json beside the running program when it exists.
func defaultBundled() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	p := filepath.Join(filepath.Dir(exe), "bundled", "index.json")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func run(configPath string, safeMode bool, watch time.Duration, bundled string) error {
	if bundled == "" {
		bundled = defaultBundled()
	} else if abs, err := filepath.Abs(bundled); err == nil {
		bundled = abs
	}
	c, err := core.Start(core.Options{ConfigPath: configPath, SafeMode: safeMode, Bundled: bundled})
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
	// Extension pages have a listener of their own, so they are served on another site than
	// the panel (next/ui/extension-pages/README.md).
	pagesLn, err := net.Listen("tcp", cfg.ExtensionPages.Listen)
	if err != nil {
		ln.Close()
		return fmt.Errorf("extension_pages.listen: %w", err)
	}
	c.SetPagesPort(pagesLn.Addr().(*net.TCPAddr).Port)
	managementOn := func() bool { return c.Config().ManagementKey != "" }
	mux := http.NewServeMux()
	mux.Handle(core.ManagementPrefix, c.Management())
	// The panel is on while the management API is: it is a page for that API.
	assets := panel.Assets()
	if !panel.Built(assets) {
		log.Print("warning: this core was built without its panel (go generate ./internal/panel builds it)")
	}
	ui := panel.Handler(assets, managementOn, func(r *http.Request) string {
		origin, _ := c.PagesOrigin(r.Host) // none: the panel says why where it would show a page
		return origin
	})
	mux.Handle(strings.TrimSuffix(panel.Prefix, "/"), ui)
	mux.Handle(panel.Prefix, ui)
	mux.Handle("/", server.NewHandler(c))
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 30 * time.Second,
	}
	pagesSrv := &http.Server{
		Handler:           pages.Handler(c.PagesFolder, managementOn),
		ReadHeaderTimeout: 30 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "pluxy: listening on http://%s\n", ln.Addr())
	fmt.Fprintf(os.Stderr, "pluxy: extension pages on http://%s\n", pagesLn.Addr())

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

	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(ln) }()
	go func() { errc <- pagesSrv.Serve(pagesLn) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = pagesSrv.Shutdown(shutdownCtx) // pages are static files: nothing to finish
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	srv.Close()
	pagesSrv.Close()
	return nil
}
