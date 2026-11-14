// Command pluxy is the pluxy core: it serves the client APIs and answers through a provider
// extension.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/stdotwork/pluxy/next/internal/buildinfo"
	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/internal/legacy"
	"github.com/stdotwork/pluxy/next/internal/logfile"
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
	importAuth := flag.String("import-auth", "", "copy the credential files of today's core's auth folder at this path into the credential store, then exit (run it while core is stopped)")
	version := flag.Bool("version", false, "print the build's version, commit and build date, then exit")
	listenFD := flag.Int("listen-fd", -1, "serve on the listening TCP socket inherited at this file descriptor instead of binding listen (for a program that starts core and keeps the socket)")
	flag.Parse()
	if *version {
		fmt.Println(buildinfo.Line())
		return
	}
	if *importAuth != "" {
		if err := importAuthDir(*configPath, *importAuth); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		return
	}
	if err := run(*configPath, *safeMode, *watch, *bundled, *listenFD); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// importAuthDir copies the credential files in today's core's auth folder dir into the
// credential store of the config at configPath, and says what it imported and skipped.
func importAuthDir(configPath, dir string) error {
	cfg, err := server.LoadConfig(configPath)
	if err != nil {
		return core.ScrubConfigError(configPath, err)
	}
	if err := legacy.CheckImportFolders(dir, cfg.CredentialsDir); err != nil {
		return err
	}
	files, skipped, err := legacy.ReadAuthDir(dir, cfg.Import, time.Now())
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	store, err := cred.Open(cred.FileBackend{Dir: cfg.CredentialsDir})
	if err != nil {
		return err
	}
	reserved := map[string]bool{}
	for _, a := range cfg.Accounts() {
		reserved[strings.ToLower(a.ID)] = true
	}
	imported, more, err := legacy.Import(store, files, func(id string) bool { return reserved[strings.ToLower(id)] })
	for _, f := range imported {
		line := fmt.Sprintf("imported %s as account %s (%s)", f.File, f.Record.ID, f.Record.Extension)
		if len(f.Ignored) > 0 {
			line += "; not supported yet, not imported: " + strings.Join(f.Ignored, ", ")
		}
		fmt.Println(line)
	}
	for _, s := range append(skipped, more...) {
		fmt.Printf("skipped %s: %s\n", s.File, s.Reason)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%d imported, %d skipped, into %s\n", len(imported), len(skipped)+len(more), cfg.CredentialsDir)
	return nil
}

// listen binds addr, or with fd 0 or more serves on the listening TCP socket inherited at fd.
// The program that started core keeps its own copy of that socket, so no other program can bind
// the address while core stops (it closes its listener as it starts draining) or after it exits.
func listen(addr string, fd int) (net.Listener, error) {
	if fd < 0 {
		return net.Listen("tcp", addr)
	}
	f := os.NewFile(uintptr(fd), "listen-fd")
	if f == nil {
		return nil, fmt.Errorf("-listen-fd %d: not an open file", fd)
	}
	// FileListener keeps a duplicate that closes on exec, so programs core starts never hold the
	// socket; the inherited descriptor is closed here.
	defer f.Close()
	ln, err := net.FileListener(f)
	if err != nil {
		return nil, fmt.Errorf("-listen-fd %d: %w", fd, err)
	}
	if _, ok := ln.(*net.TCPListener); !ok {
		ln.Close()
		return nil, fmt.Errorf("-listen-fd %d: not a TCP socket", fd)
	}
	return ln, nil
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

func run(configPath string, safeMode bool, watch time.Duration, bundled string, listenFD int) error {
	if bundled == "" {
		bundled = defaultBundled()
	} else if abs, err := filepath.Abs(bundled); err == nil {
		bundled = abs
	}
	// Core's log goes to standard error and, while the config says so, to its logs folder.
	logs := logfile.New(core.LogsDir(configPath))
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	log.Print(buildinfo.Line())
	c, err := core.Start(core.Options{ConfigPath: configPath, SafeMode: safeMode, Bundled: bundled, Logs: logs})
	if err != nil {
		if !safeMode {
			err = fmt.Errorf("%w (start with -safe-mode to run without extensions)", err)
		}
		return err
	}
	defer c.Stop()
	cfg := c.Config()
	if safeMode {
		log.Print("safe mode: no extension runs")
	}
	for _, e := range c.Status().Extensions {
		log.Printf("extension %s %s started", e.ID, e.Version)
	}

	ln, err := listen(cfg.Listen, listenFD)
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
	tlsConfig, err := cfg.TLSConfig()
	if err != nil {
		ln.Close()
		pagesLn.Close()
		return err
	}
	srv := &http.Server{
		Handler:           server.WithClientIP(c.Config, mux),
		ReadHeaderTimeout: 30 * time.Second,
		TLSConfig:         tlsConfig,
	}
	pagesSrv := &http.Server{
		Handler:           pages.Handler(c.PagesFolder, managementOn),
		ReadHeaderTimeout: 30 * time.Second,
	}
	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}
	fmt.Fprintf(os.Stderr, "pluxy: listening on %s://%s\n", scheme, ln.Addr())
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
	go func() {
		if tlsConfig != nil {
			errc <- srv.ServeTLS(ln, "", "") // the certificate comes from TLSConfig; HTTP/2 is on
			return
		}
		errc <- srv.Serve(ln)
	}()
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
