package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// target is the core a client subcommand calls.
type target struct {
	url, key string
	http     *http.Client
}

// findCore returns the core to call: at addr with key when both are set, else what the config
// file at configPath says for the one missing: its listen address (on this machine when it
// listens on every interface) and its management key. A core serving TLS with its own
// certificate is called with that certificate trusted.
func findCore(configPath, addr, key string) (target, error) {
	if addr != "" {
		u, err := url.Parse(addr)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return target{}, usageError{fmt.Sprintf("--url %q must start with http:// or https://, such as http://127.0.0.1:8317", addr)}
		}
	}
	t := target{url: addr, key: key, http: &http.Client{Timeout: 2 * time.Minute}}
	if addr != "" && key != "" {
		return t, nil
	}
	cfg, err := server.LoadConfig(configPath)
	if err != nil {
		if addr == "" && errors.Is(err, os.ErrNotExist) {
			return t, fmt.Errorf("no config file at %s: pass --config, or --url and --key (or set PLUXY_URL and PLUXY_MANAGEMENT_KEY)", configPath)
		}
		if addr == "" {
			return t, core.ScrubConfigError(configPath, err)
		}
		return t, nil // the key stays empty: core answers 401
	}
	if t.key == "" {
		t.key = cfg.ManagementKey
	}
	if t.url != "" {
		return t, nil
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return t, fmt.Errorf("config %s: listen %q: %w", configPath, cfg.Listen, err)
	}
	if port == "0" {
		return t, fmt.Errorf("config %s listens on port 0, a port core picks at start: pass --url with the address core printed", configPath)
	}
	if t.url, err = server.LocalURL(cfg.Listen, cfg.TLS.Enable); err != nil {
		return t, err
	}
	if cfg.TLS.Enable {
		if pool, err := x509.SystemCertPool(); err == nil {
			cert := cfg.TLS.Cert
			if !filepath.IsAbs(cert) {
				cert = filepath.Join(filepath.Dir(configPath), cert)
			}
			if pem, err := os.ReadFile(cert); err == nil && pool.AppendCertsFromPEM(pem) {
				t.http.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
			}
		}
	}
	return t, nil
}
