package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// pprofBinary returns the pluxy binary built with the pprof build tag. It is built once per run,
// in the test binaries' folder.
var (
	pprofOnce sync.Once
	pprofPath string
	pprofErr  error
)

func pprofBinary(t *testing.T) string {
	t.Helper()
	pprofOnce.Do(func() {
		dir := filepath.Join(binDir, "pprof")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			pprofErr = err
			return
		}
		pprofPath = filepath.Join(dir, "pluxy")
		out, err := exec.Command("go", "build", "-buildvcs=false", "-tags", "pprof", "-o", pprofPath, "../cmd/pluxy").CombinedOutput()
		if err != nil {
			pprofErr = fmt.Errorf("building ../cmd/pluxy with -tags pprof: %w\n%s", err, out)
		}
	})
	if pprofErr != nil {
		t.Fatal(pprofErr)
	}
	return pprofPath
}

// pprofCfg is a config with one test extension and, when pprof is not nil, the pprof section.
func pprofCfg(t *testing.T, pprof map[string]any) map[string]any {
	t.Helper()
	cfg := lifecycleConfig(testPackage(t, "0.1.0"), newGate(t), nil)
	if pprof != nil {
		cfg["pprof"] = pprof
	}
	return cfg
}

// runPprof runs the program at bin with cfg, and returns its base URL, its config path and its log.
func runPprof(t *testing.T, bin string, cfg map[string]any) (string, string, *coreLog) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, cfg)
	base, logs := runCoreBin(t, bin, path, noWatch...)
	return base, path, logs
}

var pprofClient = &http.Client{Timeout: 10 * time.Second}

// pprofGet GETs url and returns the status and the body.
func pprofGet(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := pprofClient.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// listening reports whether something accepts connections at addr.
func listening(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Off by default: core opens no profiling listener, on the default address or any other, and
// answers no profiling page on its client and management listener.
func TestPprofOffByDefault(t *testing.T) {
	base, _, logs := runCoreArgs(t, pprofCfg(t, nil), noWatch...)
	all := logs.waitFor(t, "extension pages on")
	if strings.Contains(all, "pprof") {
		t.Errorf("core logged about pprof with it off:\n%s", all)
	}
	if code, _ := pprofGet(t, base+"/debug/pprof/"); code != http.StatusNotFound {
		t.Errorf("GET /debug/pprof/ = %d, want 404", code)
	}
	if listening("127.0.0.1:8316") {
		t.Errorf("something accepts connections at 127.0.0.1:8316, the default pprof address: this check is inconclusive")
	}
}

// The default build has no profiling pages. With the setting on it says so once, listens on
// nothing and answers no profiling page.
func TestPprofDefaultBuildSaysItHasNoProfiling(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	base, _, logs := runCoreArgs(t, pprofCfg(t, map[string]any{"enable": true, "addr": addr}), noWatch...)
	all := logs.waitFor(t, "extension pages on")
	if !strings.Contains(all, "go build -tags pprof ./cmd/pluxy") {
		t.Errorf("core did not say how to get profiling pages; log:\n%s", all)
	}
	if listening(addr) {
		t.Errorf("the default build listens on %s", addr)
	}
	if code, _ := pprofGet(t, base+"/debug/pprof/"); code != http.StatusNotFound {
		t.Errorf("GET /debug/pprof/ = %d, want 404", code)
	}
}

// With the setting on, the profiling pages answer on their own loopback listener, and only there.
func TestPprofServesOnItsOwnLoopbackListener(t *testing.T) {
	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	base, _, logs := runPprof(t, pprofBinary(t), pprofCfg(t, map[string]any{"enable": true, "addr": addr}))
	logs.waitFor(t, "pprof: listening on http://"+addr)

	code, body := pprofGet(t, "http://"+addr+"/debug/pprof/")
	if code != http.StatusOK || !strings.Contains(string(body), "heap") {
		t.Errorf("GET %s/debug/pprof/ = %d with %d bytes, want 200 listing the profiles", addr, code, len(body))
	}
	code, body = pprofGet(t, "http://"+addr+"/debug/pprof/heap")
	if code != http.StatusOK || len(body) == 0 {
		t.Errorf("GET %s/debug/pprof/heap = %d with %d bytes, want 200 and a profile", addr, code, len(body))
	}
	if code, _ := pprofGet(t, base+"/debug/pprof/"); code != http.StatusNotFound {
		t.Errorf("GET /debug/pprof/ on the client and management listener = %d, want 404", code)
	}
}

// An address that is not loopback fails the config load, and core does not start.
func TestPprofRefusesNonLoopbackAddressAtLoad(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8316", "192.0.2.1:8316"} {
		path := filepath.Join(t.TempDir(), "config.json")
		writeConfig(t, path, pprofCfg(t, map[string]any{"enable": true, "addr": addr}))
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out, err := exec.CommandContext(ctx, filepath.Join(binDir, "pluxy"), "-config", path).CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), "loopback") || strings.Contains(string(out), "listening on") {
			t.Errorf("pprof addr %s: core exited with %v:\n%s\nwant exit status 1 naming the loopback rule, before listening", addr, err, out)
		}
	}
}

// A reload that sets a non-loopback address, or an address another program holds, is refused: the
// old listener keeps serving and the active revision stays.
func TestPprofReloadRefusedKeepsListener(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	cfg := pprofCfg(t, map[string]any{"enable": true, "addr": addr})
	base, path, logs := runPprof(t, pprofBinary(t), cfg)
	logs.waitFor(t, "pprof: listening on http://"+addr)

	check := func(what, wantErr string) {
		t.Helper()
		code, s := reload(t, base)
		if code != http.StatusUnprocessableEntity || s.Active.Revision != 1 || !strings.Contains(s.Desired.Error, wantErr) {
			t.Errorf("%s: reload %d, active %d, error %q; want 422, active revision 1 and an error naming %q",
				what, code, s.Active.Revision, s.Desired.Error, wantErr)
		}
		if code, _ := pprofGet(t, "http://"+addr+"/debug/pprof/"); code != http.StatusOK {
			t.Errorf("%s: pprof on %s answers %d after the refused reload; want 200", what, addr, code)
		}
	}

	cfg["pprof"] = map[string]any{"enable": true, "addr": fmt.Sprintf("0.0.0.0:%d", freePort(t))}
	writeConfig(t, path, cfg)
	check("non-loopback address", "loopback")

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cfg["pprof"] = map[string]any{"enable": true, "addr": held.Addr().String()}
	writeConfig(t, path, cfg)
	check("address another program holds", "pprof.addr")
}

// A reload starts the listener when the setting turns on, moves it when the address changes, and
// closes it when the setting turns off.
func TestPprofReloadStartsMovesAndStops(t *testing.T) {
	a := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	b := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cfg := pprofCfg(t, nil)
	base, path, _ := runPprof(t, pprofBinary(t), cfg)

	apply := func(what string, revision int, pprof map[string]any) {
		t.Helper()
		cfg["pprof"] = pprof
		writeConfig(t, path, cfg)
		code, s := reload(t, base)
		if code != http.StatusOK || s.Active.Revision != revision || s.Desired.Error != "" {
			t.Fatalf("%s: reload %d, active %d, error %q; want 200 and active revision %d",
				what, code, s.Active.Revision, s.Desired.Error, revision)
		}
	}

	apply("turn on", 2, map[string]any{"enable": true, "addr": a})
	if code, _ := pprofGet(t, "http://"+a+"/debug/pprof/"); code != http.StatusOK {
		t.Errorf("after turning on: pprof on %s answers %d; want 200", a, code)
	}

	apply("move", 3, map[string]any{"enable": true, "addr": b})
	if listening(a) {
		t.Errorf("after the move: the old address %s still accepts connections", a)
	}
	if code, _ := pprofGet(t, "http://"+b+"/debug/pprof/"); code != http.StatusOK {
		t.Errorf("after the move: pprof on %s answers %d; want 200", b, code)
	}

	apply("turn off", 4, map[string]any{"enable": false, "addr": b})
	if listening(b) {
		t.Errorf("after turning off: %s still accepts connections", b)
	}
}
