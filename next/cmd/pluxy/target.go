package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/internal/home"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// target is the core a client subcommand calls, and where its address and key came from.
type target struct {
	url, key string
	http     *http.Client
	// urlFrom and keyFrom name where the address and the key came from: --url, PLUXY_URL,
	// --key, PLUXY_MANAGEMENT_KEY, or config, the config file read; empty for a key there was
	// none of.
	urlFrom, keyFrom string
	// config is the config file read, "" when none was.
	config string
	// configFrom says how config was chosen.
	configFrom configSource
	// shadowed is pluxy's home's config when it exists and config.json in the current folder
	// was read before it.
	shadowed string
}

// configSource says how the config file a command read was chosen.
type configSource int

const (
	fromFlag   configSource = iota + 1 // --config
	fromFolder                         // config.json in the current folder
	fromHome                           // config.json in pluxy's home
)

// fromConfig is urlFrom's and keyFrom's value for the config file.
const fromConfig = "config"

// where is what the command line and the environment say about the core to call.
type where struct {
	// configFlag is --config, "" when it was not given.
	configFlag       string
	urlFlag, keyFlag string
	getenv           func(string) string
	// local: the command calls no core, so the address is neither needed nor checked.
	local bool
}

// exists reports whether there is a file or folder at path; one that cannot be looked at is
// taken to be there, so the program reading it says what is wrong with it.
func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// defaultConfig is the config file pluxy serve, pluxy init and the client commands read when
// no --config is given: config.json in the current folder when there is one, else config.json
// in pluxy's home (internal/home). err is set only when pluxy's home cannot be found.
func defaultConfig(getenv func(string) string) (path string, src configSource, err error) {
	if exists(home.ConfigName) {
		return home.ConfigName, fromFolder, nil
	}
	p, err := home.Config(getenv)
	return p, fromHome, err
}

// findCore returns the core to call. The address and the key each come from the first of:
// the flag (--url, --key), the environment (PLUXY_URL, PLUXY_MANAGEMENT_KEY), and the config
// file, which is --config, else config.json in the current folder, else config.json in pluxy's
// home. From the config come its listen address (on this machine when it listens on every
// interface) and its management key. A core serving TLS with its own certificate is called with
// that certificate trusted.
//
// A key from --key or PLUXY_MANAGEMENT_KEY is never sent to the address of a config.json that
// only the current folder put there, unless that file holds the same key: a file in a folder
// could name any machine. The refusal is a usageError, made before any request, and only for a
// config that loads and has a usable address: one with no management key, or listening on port
// 0, fails first with its own error (exit 1) and sends nothing either. When the current folder
// is pluxy's home, its config.json is a folder file and is checked. --config, --url and
// PLUXY_URL are a person's own choice of address and are not refused.
func findCore(w where) (target, error) {
	t := target{http: &http.Client{Timeout: 2 * time.Minute}}
	t.url, t.urlFrom = firstSet(w.urlFlag, "--url", w.getenv("PLUXY_URL"), "PLUXY_URL")
	t.key, t.keyFrom = firstSet(w.keyFlag, "--key", w.getenv("PLUXY_MANAGEMENT_KEY"), "PLUXY_MANAGEMENT_KEY")
	if t.url != "" {
		u, err := url.Parse(t.url)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return target{}, usageError{fmt.Sprintf("--url %q must start with http:// or https://, such as http://127.0.0.1:8317", t.url)}
		}
	}
	needURL := t.url == "" && !w.local
	keyGiven := t.key != ""
	if !needURL && t.key != "" {
		return t, nil
	}
	path, src, err := pickConfig(w)
	if err != nil {
		if needURL || w.local {
			return t, err
		}
		return t, nil // the key stays empty: core answers 401
	}
	t.config, t.configFrom = path, src
	if src == fromFolder {
		if hp, err := home.Config(w.getenv); err == nil && exists(hp) {
			t.shadowed = hp
		}
	}
	cfg, err := server.LoadConfig(path)
	if err != nil {
		switch {
		case !needURL && !w.local:
			return t, nil // the key stays empty: core answers 401
		case errors.Is(err, os.ErrNotExist) && w.local:
			return t, notFoundError{fmt.Sprintf("no management key: none was given with --key or PLUXY_MANAGEMENT_KEY, and there is no config file at %s. Check the --config path, or pass --key", path)}
		case errors.Is(err, os.ErrNotExist):
			return t, fmt.Errorf("no config file at %s. Check the --config path, run pluxy init -config %s, or pass --url and --key", path, path)
		}
		return t, core.ScrubConfigError(path, err)
	}
	if t.key == "" {
		t.key, t.keyFrom = cfg.ManagementKey, fromConfig
	}
	if !needURL {
		return t, nil
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return t, fmt.Errorf("config %s: listen %q: %w", path, cfg.Listen, err)
	}
	if port == "0" {
		return t, fmt.Errorf("config %s listens on port 0, a port core picks at start: pass --url with the address core printed", path)
	}
	if t.url, err = server.LocalURL(cfg.Listen, cfg.TLS.Enable); err != nil {
		return t, err
	}
	t.urlFrom = fromConfig
	if keyGiven && src == fromFolder && !sameKey(t.key, cfg.ManagementKey) {
		return target{}, usageError{fmt.Sprintf("not sending the management key from %s to %s, the address in this folder's config.json, which holds a different key. "+
			"To use that pluxy, leave out %s so the key in its config.json is used. To use another, add --url <its address>.", t.keyFrom, t.url, t.keyFrom)}
	}
	if cfg.TLS.Enable {
		if pool, err := x509.SystemCertPool(); err == nil {
			cert := cfg.TLS.Cert
			if !filepath.IsAbs(cert) {
				cert = filepath.Join(filepath.Dir(path), cert)
			}
			if pem, err := os.ReadFile(cert); err == nil && pool.AppendCertsFromPEM(pem) {
				t.http.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
			}
		}
	}
	return t, nil
}

// sameKey reports whether a and b are the same key: it compares their SHA-256 digests in
// constant time, so the keys must match exactly.
func sameKey(a, b string) bool {
	da, db := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(da[:], db[:]) == 1
}

// firstSet returns the first of a and b that is set, with its name.
func firstSet(a, aName, b, bName string) (string, string) {
	switch {
	case a != "":
		return a, aName
	case b != "":
		return b, bName
	}
	return "", ""
}

// pickConfig returns the config file to read: --config, else config.json in the current
// folder, else config.json in pluxy's home. Its error names every place it looked and what to
// run when there is no config in any of them.
func pickConfig(w where) (string, configSource, error) {
	if w.configFlag != "" {
		return w.configFlag, fromFlag, nil
	}
	path, src, err := defaultConfig(w.getenv)
	if err == nil && (src == fromFolder || exists(path)) {
		return path, src, nil
	}
	var b strings.Builder
	if w.local {
		b.WriteString("no management key: none was given with --key or PLUXY_MANAGEMENT_KEY, and there is no config.json in this folder")
	} else {
		b.WriteString("no core to call: there is no config.json in this folder")
	}
	if err != nil {
		fmt.Fprintf(&b, ", and pluxy's home cannot be found (%v)", err)
	} else {
		fmt.Fprintf(&b, ", and none in pluxy's home, %s", path)
		if w.getenv(home.Env) != "" {
			fmt.Fprintf(&b, " (from %s)", home.Env)
		}
	}
	other := "--url and --key"
	if w.local {
		other = "--key"
	}
	app, aerr := home.AppCoreConfig(w.getenv)
	if aerr != nil || !exists(app) {
		fmt.Fprintf(&b, ". Start pluxy with pluxy serve, which writes its first config in pluxy's home (pluxy init writes it without starting), or pass --config <file>, or %s", other)
	} else {
		// A plain pluxy serve writes no first config beside the app's core (firstStart), so it is
		// not offered.
		fmt.Fprintf(&b, ". The desktop app keeps its own core's config at %s: pass --config %s to use that core "+
			"(to start it while the app does not run it: pluxy serve -config %s). "+
			"To make a new, separate pluxy in pluxy's home, run pluxy init; or pass --config <file>, or %s", app, quoteArg(app), quoteArg(app), other)
	}
	if w.local {
		return "", 0, notFoundError{b.String()}
	}
	return "", 0, errors.New(b.String())
}

// quoteArg quotes a path for a command line a person pastes, when it holds a space.
func quoteArg(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// configName names the config file a command read, as a person finds it.
func (t target) configName() string {
	switch t.configFrom {
	case fromFolder:
		abs, err := filepath.Abs(t.config)
		if err != nil {
			abs = t.config
		}
		return abs + " (config.json in this folder)"
	case fromHome:
		return t.config + " (pluxy's home)"
	}
	return t.config + " (--config)"
}

// describe says which core a command calls and where its address and key came from: what
// --verbose prints.
func (t target) describe() string {
	from := func(name string) string {
		if name == fromConfig {
			return t.configName()
		}
		return name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "core at %s", dash(t.url))
	switch {
	case t.urlFrom == t.keyFrom && t.urlFrom != "":
		fmt.Fprintf(&b, ", its address and management key from %s", from(t.urlFrom))
	default:
		if t.urlFrom != "" {
			fmt.Fprintf(&b, ", its address from %s", from(t.urlFrom))
		}
		if t.keyFrom != "" {
			fmt.Fprintf(&b, ", its management key from %s", from(t.keyFrom))
		} else {
			b.WriteString(", with no management key")
		}
	}
	if t.shadowed != "" && (t.urlFrom == fromConfig || t.keyFrom == fromConfig) {
		fmt.Fprintf(&b, "; pluxy's home has a config too, %s, which was not read: config.json in the current folder comes first", t.shadowed)
	}
	return b.String()
}
