// Command pluxy is the pluxy core: pluxy serve (or pluxy with no subcommand) serves the client
// APIs and answers through a provider extension; every other subcommand is a client of a running
// core's management API (next/management/README.md).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	args := os.Args[1:]
	switch {
	case len(args) == 0 || strings.HasPrefix(args[0], "-"):
		serve(args)
	case args[0] == "serve":
		serve(args[1:])
	case args[0] == "init":
		os.Exit(initConfig(args[1:], os.Stdout, os.Stderr))
	default:
		// Every other subcommand is a client of a running core's management API (cli.go).
		os.Exit(runCLI(args, os.Stdout, os.Stderr, os.Getenv))
	}
}

// serve runs core: pluxy serve, or pluxy with no subcommand.
func serve(args []string) {
	fs := flag.NewFlagSet("pluxy serve", flag.ExitOnError)
	configPath := fs.String("config", "config.json", "path to the config file")
	safeMode := fs.Bool("safe-mode", false, "start no extensions: client requests fail, the management API works")
	watch := fs.Duration("watch-interval", time.Second, "how often to look for config file changes (0: do not watch)")
	bundled := fs.String("bundled", "", "index of the registry of bundled packages (default: bundled/index.json beside this program, when it exists)")
	importAuth := fs.String("import-auth", "", "copy the credential files of today's core's auth folder at this path into the credential store, then exit (run it while core is stopped)")
	version := fs.Bool("version", false, "print the build's version, commit and build date, then exit")
	listenFD := fs.Int("listen-fd", -1, "serve on the listening TCP socket inherited at this file descriptor instead of binding listen (for a program that starts core and keeps the socket)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: pluxy [serve] [flags]   run core; pluxy help lists the client commands")
		fs.PrintDefaults()
	}
	_ = fs.Parse(args) // ExitOnError
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "pluxy: unexpected argument %q (pluxy help lists the commands)\n", fs.Arg(0))
		os.Exit(exitUsage)
	}
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
// credential store of the config at configPath, and says what it imported and skipped. The
// config's notices (settings it ignores, a hashed management key) go to standard error, as
// core's log does at start, so a script reading the counts on standard output does not see them.
func importAuthDir(configPath, dir string) error {
	cfg, err := server.LoadConfig(configPath)
	if err != nil {
		return core.ScrubConfigError(configPath, err)
	}
	for _, n := range cfg.Notices {
		log.Print(n)
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
	if fd <= 2 {
		return nil, fmt.Errorf("-listen-fd %d: a standard stream is not a listening socket", fd)
	}
	f := os.NewFile(uintptr(fd), "listen-fd")
	if f == nil {
		return nil, fmt.Errorf("-listen-fd %d: not an open file", fd)
	}
	// FileListener keeps a duplicate that closes on exec and the inherited descriptor is closed
	// here, so programs core starts after this never hold the socket (run takes it before any
	// extension starts).
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

// initPortEnv names the port a config written at a first start listens on, in place of 8317 or
// a free one; 0 is a free one. Only for tests, which must never take 8317 on the machine they
// run on.
const initPortEnv = "PLUXY_INIT_PORT"

// firstStart writes a first config at configPath when there is no file there, and says where
// it is: a new install starts with no config. It logs the file's path, never its keys.
func firstStart(configPath string) error {
	if _, err := os.Stat(configPath); !errors.Is(err, fs.ErrNotExist) {
		return nil // core reads it, and says what is wrong with it
	}
	port := -1
	if v, ok := os.LookupEnv(initPortEnv); ok {
		p, err := strconv.Atoi(v)
		if err != nil || p < 0 {
			return fmt.Errorf("%s=%q is not a port", initPortEnv, v)
		}
		port = p
	}
	fc, err := core.WriteFirstConfig(configPath, port)
	if err != nil {
		return fmt.Errorf("no config at %s, and a first one could not be written: %w", configPath, err)
	}
	log.Printf("first start: wrote a new config, %s, listening on %s; its client key and management key are in that file", fc.Path, fc.Listen)
	return nil
}

// initConfig is pluxy init: it writes a first config, as a first start does, without starting
// core, and returns the exit code.
func initConfig(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pluxy init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "config.json", "where to write the config file; an existing file is never replaced")
	port := fs.Int("port", -1, "the port core listens on (default: 8317, or a free port when 8317 is taken; 0: a free port)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: pluxy init [-config file] [-port n]   write a first config: listening on 127.0.0.1, with a new client key and management key")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 || *port < -1 {
		fs.Usage()
		return exitUsage
	}
	fc, err := core.WriteFirstConfig(*configPath, *port)
	if err != nil {
		fmt.Fprintf(stderr, "pluxy: %v\n", err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "wrote %s: core listens on %s; its client key and management key are in that file\n", fc.Path, fc.Listen)
	return exitOK
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
	// The inherited socket is taken before any extension starts: until then its descriptor does
	// not close on exec, and every extension would hold the core's port.
	var ln net.Listener
	if listenFD >= 0 {
		l, err := listen("", listenFD)
		if err != nil {
			return err
		}
		ln = l
	}
	if err := firstStart(configPath); err != nil {
		if ln != nil {
			ln.Close()
		}
		return err
	}
	c, err := core.Start(core.Options{ConfigPath: configPath, SafeMode: safeMode, Bundled: bundled, Logs: logs})
	if err != nil {
		if ln != nil {
			ln.Close()
		}
		// Safe mode reads the config and opens the credential store too, so it is no way past
		// their errors.
		var store *cred.OpenError
		var config *core.ConfigError
		if !safeMode && !errors.As(err, &store) && !errors.As(err, &config) {
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

	if ln == nil {
		if ln, err = listen(cfg.Listen, -1); err != nil {
			return err
		}
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
	// Signals are handled before core says where it listens: whatever started it may ask it to
	// stop (or reload) as soon as it has said so, and must find it draining, not killed.
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

	fmt.Fprintf(os.Stderr, "pluxy: listening on %s://%s\n", scheme, ln.Addr())
	fmt.Fprintf(os.Stderr, "pluxy: extension pages on http://%s\n", pagesLn.Addr())

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
