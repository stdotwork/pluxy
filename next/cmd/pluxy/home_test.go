package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/internal/server"
)

// testHome is a home folder of a test's own: the environment that finds it, and where pluxy's
// home config and the desktop app's own core's config are under it (written out from the
// documented rule, not computed the way the code computes them).
type testHome struct {
	env     map[string]string
	homeCfg string // $XDG_DATA_HOME/pluxy/config.json
	appCfg  string // $XDG_DATA_HOME/dev.pluxy.desktop/core/config.json
}

func newTestHome(t *testing.T) *testHome {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "data")
	return &testHome{
		env:     map[string]string{"HOME": root, "XDG_DATA_HOME": data},
		homeCfg: filepath.Join(data, "pluxy", "config.json"),
		appCfg:  filepath.Join(data, "dev.pluxy.desktop", "core", "config.json"),
	}
}

func (h *testHome) getenv(k string) string { return h.env[k] }

// setenv puts the home's variables in this process's environment, for code that reads it there
// (pluxy serve, and the first start it makes).
func (h *testHome) setenv(t *testing.T) {
	t.Helper()
	for k, v := range h.env {
		t.Setenv(k, v)
	}
}

// writeConfig writes a config listening on 127.0.0.1:port with the management key key.
func writeConfig(t *testing.T, path string, port int, key string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`{"listen": "127.0.0.1:%d", "client_keys": ["fake-client-key-1"], "management_key": %q}`, port, key)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// emptyFolder makes the test run in a new folder with no config.json.
func emptyFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return dir
}

func mustContain(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s: %q does not say %q", what, got, w)
		}
	}
}

// A client command finds core from the flags, then the environment, then --config, else
// config.json in the current folder, else config.json in pluxy's home; and when there is none,
// it says each place it looked and what to run.
func TestFindCoreOrder(t *testing.T) {
	t.Run("nothing anywhere", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		_, err := findCore(where{getenv: h.getenv})
		if err == nil {
			t.Fatal("found a core with no config anywhere")
		}
		mustContain(t, "no config", err.Error(), "no config.json in this folder", h.homeCfg, "pluxy serve", "pluxy init", "--config <file>", "--url and --key")
		if strings.Contains(err.Error(), "desktop app") {
			t.Errorf("no config: %v names the desktop app, which has no core here", err)
		}
	})
	t.Run("PLUXY_HOME names the home", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		other := filepath.Join(t.TempDir(), "elsewhere")
		h.env["PLUXY_HOME"] = other
		writeConfig(t, h.homeCfg, 18401, "fake-default-home-key") // the default home is not read
		_, err := findCore(where{getenv: h.getenv})
		if err == nil {
			t.Fatal("PLUXY_HOME names an empty folder, and a core was found")
		}
		mustContain(t, "empty PLUXY_HOME", err.Error(), filepath.Join(other, "config.json"), "(from PLUXY_HOME)")
		writeConfig(t, filepath.Join(other, "config.json"), 18402, "fake-pluxy-home-key")
		got, err := findCore(where{getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18402" || got.key != "fake-pluxy-home-key" || got.configFrom != fromHome {
			t.Errorf("PLUXY_HOME: %+v %v", got, err)
		}
		h.env["PLUXY_HOME"] = "relative/home"
		_, err = findCore(where{getenv: h.getenv})
		if err == nil || !strings.Contains(err.Error(), `PLUXY_HOME="relative/home" is not an absolute path`) {
			t.Errorf("relative PLUXY_HOME: %v", err)
		}
	})
	t.Run("only the app's own core", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		writeConfig(t, h.appCfg, 18403, "fake-app-core-key")
		_, err := findCore(where{getenv: h.getenv})
		if err == nil {
			t.Fatal("the app's own core was called without --config")
		}
		mustContain(t, "app's core", err.Error(), h.homeCfg, "desktop app", "--config "+h.appCfg, "pluxy init", "pluxy serve -config "+h.appCfg)
		// A plain pluxy serve writes no config beside the app's core (TestNoSecondPluxyBesideTheApp),
		// so the error offers pluxy serve only with the app's -config.
		if n, withConfig := strings.Count(err.Error(), "pluxy serve"), strings.Count(err.Error(), "pluxy serve -config "); n != withConfig {
			t.Errorf("app's core: %v offers a plain pluxy serve, which refuses here", err)
		}
	})
	t.Run("pluxy's home from any folder", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		writeConfig(t, h.homeCfg, 18404, "fake-home-key")
		got, err := findCore(where{getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18404" || got.key != "fake-home-key" || got.config != h.homeCfg || got.configFrom != fromHome || got.shadowed != "" {
			t.Fatalf("home: %+v %v", got, err)
		}
		mustContain(t, "describe", got.describe(), "core at http://127.0.0.1:18404", "its address and management key from "+h.homeCfg+" (pluxy's home)")
	})
	t.Run("this folder's config.json comes before pluxy's home", func(t *testing.T) {
		h := newTestHome(t)
		writeConfig(t, h.homeCfg, 18405, "fake-home-key")
		dir := emptyFolder(t)
		writeConfig(t, filepath.Join(dir, "config.json"), 18406, "fake-folder-key")
		got, err := findCore(where{getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18406" || got.key != "fake-folder-key" || got.configFrom != fromFolder || got.shadowed != h.homeCfg {
			t.Fatalf("folder and home: %+v %v", got, err)
		}
		mustContain(t, "describe", got.describe(), filepath.Join(dir, "config.json")+" (config.json in this folder)", "pluxy's home has a config too, "+h.homeCfg)
		// pluxy serve and pluxy init choose the same way.
		if path, src, err := defaultConfig(h.getenv); err != nil || path != "config.json" || src != fromFolder {
			t.Errorf("defaultConfig with config.json here: %q %v %v", path, src, err)
		}
		// --config comes before both.
		named := filepath.Join(t.TempDir(), "named.json")
		writeConfig(t, named, 18407, "fake-named-key")
		got, err = findCore(where{configFlag: named, getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18407" || got.key != "fake-named-key" || got.configFrom != fromFlag {
			t.Errorf("--config: %+v %v", got, err)
		}
		// The environment comes before any config file, which is then not read.
		h.env["PLUXY_URL"], h.env["PLUXY_MANAGEMENT_KEY"] = "http://127.0.0.1:18408", "fake-env-key"
		got, err = findCore(where{configFlag: named, getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18408" || got.key != "fake-env-key" || got.config != "" || got.urlFrom != "PLUXY_URL" || got.keyFrom != "PLUXY_MANAGEMENT_KEY" {
			t.Errorf("environment: %+v %v", got, err)
		}
		// The flags come before the environment.
		got, err = findCore(where{urlFlag: "http://127.0.0.1:18409", keyFlag: "fake-flag-key", getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18409" || got.key != "fake-flag-key" || got.urlFrom != "--url" || got.keyFrom != "--key" {
			t.Errorf("flags: %+v %v", got, err)
		}
	})
	t.Run("an address given, the key from pluxy's home", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		writeConfig(t, h.homeCfg, 18410, "fake-home-key")
		h.env["PLUXY_URL"] = "http://127.0.0.1:18411"
		got, err := findCore(where{getenv: h.getenv})
		if err != nil || got.url != "http://127.0.0.1:18411" || got.key != "fake-home-key" || got.urlFrom != "PLUXY_URL" || got.keyFrom != fromConfig {
			t.Fatalf("address given: %+v %v", got, err)
		}
		mustContain(t, "describe", got.describe(), "its address from PLUXY_URL", "its management key from "+h.homeCfg)
	})
}

// --verbose says on standard error which core a command called and where its address and key
// came from; without it nothing is said.
func TestVerboseNamesTheConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-home-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"version":"dev"}`))
	}))
	defer srv.Close()
	h := newTestHome(t)
	emptyFolder(t)
	writeConfig(t, h.homeCfg, srv.Listener.Addr().(*net.TCPAddr).Port, "fake-home-key")
	var out, errOut bytes.Buffer
	if code := runCLIWithInput([]string{"status", "--verbose"}, strings.NewReader(""), &out, &errOut, h.getenv, false); code != exitOK {
		t.Fatalf("status --verbose: %d %q", code, errOut.String())
	}
	want := fmt.Sprintf("pluxy: core at %s, its address and management key from %s (pluxy's home)\n", srv.URL, h.homeCfg)
	if errOut.String() != want {
		t.Errorf("status --verbose said\n%q, want\n%q", errOut.String(), want)
	}
	out.Reset()
	errOut.Reset()
	if code := runCLIWithInput([]string{"status"}, strings.NewReader(""), &out, &errOut, h.getenv, false); code != exitOK || errOut.Len() != 0 {
		t.Errorf("status: %d %q", code, errOut.String())
	}
}

// pluxy init writes pluxy's home's config when the current folder has no config.json, never
// replaces one, and keeps writing nowhere else when the folder has one.
func TestInitWritesPluxysHome(t *testing.T) {
	h := newTestHome(t)
	dir := emptyFolder(t)
	var out, errOut bytes.Buffer
	if code := initConfig([]string{"-port", "0"}, &out, &errOut, h.getenv); code != exitOK {
		t.Fatalf("init: %d %q", code, errOut.String())
	}
	mustContain(t, "init", out.String(), "wrote "+h.homeCfg)
	mustContain(t, "init's note", errOut.String(), "pluxy's home", "from any folder")
	st, err := os.Stat(h.homeCfg)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("home config: %v %v", st, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("init wrote config.json in the current folder too: %v", err)
	}
	first, _ := os.ReadFile(h.homeCfg)
	out.Reset()
	errOut.Reset()
	if code := initConfig([]string{"-port", "0"}, &out, &errOut, h.getenv); code != exitFailed {
		t.Errorf("init again: %d, want %d", code, exitFailed)
	}
	mustContain(t, "init again", errOut.String(), h.homeCfg+" already exists", "pluxy serve,")
	if again, _ := os.ReadFile(h.homeCfg); !bytes.Equal(first, again) {
		t.Error("init replaced pluxy's home's config")
	}

	// A folder with a config.json: init finds that one, as before, and leaves the home alone.
	h2 := newTestHome(t)
	dir2 := emptyFolder(t)
	writeConfig(t, filepath.Join(dir2, "config.json"), 18412, "fake-folder-key")
	out.Reset()
	errOut.Reset()
	if code := initConfig([]string{"-port", "0"}, &out, &errOut, h2.getenv); code != exitFailed {
		t.Errorf("init beside config.json: %d", code)
	}
	mustContain(t, "init beside config.json", errOut.String(), "already exists. Start core with: pluxy serve -config "+filepath.Join(dir2, "config.json"))
	if _, err := os.Stat(filepath.Dir(h2.homeCfg)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("init beside config.json made pluxy's home: %v", err)
	}
}

// A second pluxy is never made beside the desktop app's own core without being asked: a plain
// first start refuses and says what to run, pluxy init (asked for) writes and names the app's
// core, and a first start at a named -config is as before.
func TestNoSecondPluxyBesideTheApp(t *testing.T) {
	h := newTestHome(t)
	h.setenv(t)
	t.Setenv("PLUXY_INIT_PORT", "0") // never 8317 on the machine the test runs on
	emptyFolder(t)
	writeConfig(t, h.appCfg, 18413, "fake-app-core-key")

	err := firstStart(h.homeCfg, true, false)
	if err == nil {
		t.Fatal("a first start in pluxy's home wrote a config beside the app's own core")
	}
	mustContain(t, "refusal", err.Error(), h.homeCfg, h.appCfg, "none was written", "pluxy serve -config "+h.appCfg, "pluxy init")
	if _, err := os.Stat(filepath.Dir(h.homeCfg)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the refused first start made pluxy's home: %v", err)
	}

	named := filepath.Join(t.TempDir(), "named", "config.json")
	if err := firstStart(named, false, false); err != nil {
		t.Errorf("first start at a named -config: %v", err)
	} else if _, err := os.Stat(named); err != nil {
		t.Errorf("first start at a named -config wrote nothing: %v", err)
	}

	var out, errOut bytes.Buffer
	if code := initConfig([]string{"-port", "0"}, &out, &errOut, h.getenv); code != exitOK {
		t.Fatalf("init beside the app's core: %d %q", code, errOut.String())
	}
	mustContain(t, "init beside the app's core", errOut.String(), "The desktop app keeps a separate pluxy at "+h.appCfg)
	// Once pluxy's home has a config, a first start there writes nothing and is not refused.
	if err := firstStart(h.homeCfg, true, false); err != nil {
		t.Errorf("start with pluxy's home's config beside the app's core: %v", err)
	}
}

// pluxy key management says where the key is and prints it only when asked; pluxy key
// rotate-management rotates it through a real core, which writes the new key to its config,
// prints the new key only when asked, and refuses when it could not say where the key went.
func TestManagementKeyCommands(t *testing.T) {
	const oldKey = "fake-old-management-key"
	h := newTestHome(t)
	emptyFolder(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, h.homeCfg, ln.Addr().(*net.TCPAddr).Port, oldKey)
	c, err := core.Start(core.Options{ConfigPath: h.homeCfg, SafeMode: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	// drop loses the reply of a rotation on its way back: dropAfter once core has rotated the
	// key and written it, dropBefore before core sees the call. The connection just closes.
	const (
		dropAfter = iota + 1
		dropBefore
	)
	var drop atomic.Int32
	mgmt := c.Management()
	mux := http.NewServeMux()
	mux.Handle(core.ManagementPrefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := drop.Load()
		if mode == 0 || !strings.HasSuffix(r.URL.Path, "/management-key/rotate") {
			mgmt.ServeHTTP(w, r)
			return
		}
		if mode == dropAfter {
			mgmt.ServeHTTP(httptest.NewRecorder(), r)
		}
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			conn.Close()
		}
	}))
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()
	base := "http://" + ln.Addr().String()

	run := func(env map[string]string, stdin string, tty bool, args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		getenv := func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return h.env[k]
		}
		code := runCLIWithInput(args, strings.NewReader(stdin), &out, &errOut, getenv, tty)
		return out.String(), errOut.String(), code
	}
	fileKey := func() string {
		t.Helper()
		cfg, err := server.LoadConfig(h.homeCfg)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.ManagementKey
	}

	out, errOut, code := run(nil, "", false, "key", "management")
	if code != exitOK || strings.Contains(out+errOut, oldKey) {
		t.Errorf("key management printed the key or failed: %d %q %q", code, out, errOut)
	}
	mustContain(t, "key management", out, "management_key field of "+h.homeCfg+" (pluxy's home)")
	if out, _, code := run(nil, "", false, "key", "management", "--show-key"); code != exitOK || out != oldKey+"\n" {
		t.Errorf("key management --show-key: %d %q", code, out)
	}
	out, _, code = run(nil, "", false, "key", "management", "--json")
	var where map[string]string
	if code != exitOK || json.Unmarshal([]byte(out), &where) != nil || where["from"] != "config" || where["config"] != h.homeCfg || strings.Contains(out, oldKey) {
		t.Errorf("key management --json: %d %q", code, out)
	}
	if out, _, code := run(map[string]string{"PLUXY_MANAGEMENT_KEY": "fake-env-key"}, "", false, "key", "management"); code != exitOK || !strings.Contains(out, "PLUXY_MANAGEMENT_KEY environment variable") {
		t.Errorf("key management with the key in the environment: %d %q", code, out)
	}

	// Refused before core is called: not asked and not --yes; --json without --show-key; a core
	// not found through a config file without --show-key.
	for _, c := range []struct {
		env  map[string]string
		args []string
		want string
	}{
		{nil, []string{"key", "rotate-management"}, "pass --yes"},
		{nil, []string{"key", "rotate-management", "--yes", "--json"}, "add --show-key"},
		{map[string]string{"PLUXY_URL": base, "PLUXY_MANAGEMENT_KEY": oldKey}, []string{"key", "rotate-management", "--yes"}, "add --show-key"},
	} {
		out, errOut, code := run(c.env, "", false, c.args...)
		if code != exitUsage || out != "" || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: %d %q %q, want exit %d saying %q", c.args, code, out, errOut, exitUsage, c.want)
		}
		if k := fileKey(); k != oldKey {
			t.Fatalf("%v rotated the key", c.args)
		}
	}
	// Asked on a terminal and declined: nothing changes.
	if _, errOut, code := run(nil, "n\n", true, "key", "rotate-management"); code != exitOK || !strings.Contains(errOut, "not rotated") || fileKey() != oldKey {
		t.Errorf("declined rotation: %d %q", code, errOut)
	}

	out, errOut, code = run(nil, "", false, "key", "rotate-management", "--yes")
	newKey := fileKey()
	if code != exitOK || out != "" || newKey == oldKey || newKey == "" || strings.Contains(errOut, newKey) {
		t.Fatalf("rotate: %d %q %q (key in the file changed: %v)", code, out, errOut, newKey != oldKey)
	}
	mustContain(t, "rotate", errOut, "Core wrote the new key to "+h.homeCfg, "web panel", "desktop app", "PLUXY_MANAGEMENT_KEY")
	if _, errOut, code := run(nil, "", false, "status"); code != exitOK {
		t.Errorf("status after the rotation: %d %q", code, errOut)
	}
	if _, _, code := run(map[string]string{"PLUXY_MANAGEMENT_KEY": oldKey}, "", false, "status"); code != exitRefused {
		t.Errorf("status with the old key: %d, want %d", code, exitRefused)
	}

	// With the key from the environment and --show-key, the new key is printed once, alone, and
	// the variable is named as holding the old one; asked on a terminal and accepted.
	out, errOut, code = run(map[string]string{"PLUXY_URL": base, "PLUXY_MANAGEMENT_KEY": newKey}, "y\n", true, "key", "rotate-management", "--show-key")
	third := fileKey()
	if code != exitOK || out != third+"\n" || third == newKey {
		t.Errorf("rotate --show-key: %d %q %q", code, out, errOut)
	}
	mustContain(t, "rotate --show-key", errOut, "PLUXY_MANAGEMENT_KEY still holds the old key")

	// runTo runs with standard output going to w.
	runTo := func(w io.Writer, env map[string]string, args ...string) (string, int) {
		var errOut bytes.Buffer
		getenv := func(k string) string {
			if v, ok := env[k]; ok {
				return v
			}
			return h.env[k]
		}
		code := runCLIWithInput(args, strings.NewReader(""), w, &errOut, getenv, false)
		return errOut.String(), code
	}
	full := failingWriter{}

	// The key cannot be written out (a full disk): key management fails rather than say nothing.
	if errOut, code := runTo(full, nil, "key", "management", "--show-key"); code != exitFailed || !strings.Contains(errOut, "writing to standard output failed") {
		t.Errorf("key management --show-key to a full output: %d %q", code, errOut)
	}
	if errOut, code := runTo(full, nil, "key", "management"); code != exitFailed {
		t.Errorf("key management to a full output: %d %q", code, errOut)
	}

	// A config file with core's address and key that is not core's own file: core would write the
	// new key elsewhere, so nothing is rotated.
	key := fileKey()
	stale := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(stale, []byte(fmt.Sprintf(`{"listen": %q, "client_keys": ["fake-other-client-key"], "management_key": %q}`, ln.Addr().String(), key)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = run(nil, "", false, "key", "rotate-management", "--yes", "--config", stale)
	if code != exitUsage || out != "" || fileKey() != key {
		t.Fatalf("rotate from a stale config: %d %q %q (rotated: %v)", code, out, errOut, fileKey() != key)
	}
	mustContain(t, "rotate from a stale config", errOut, "does not run on "+stale, "--show-key")

	// A copy with the same contents cannot be told apart beforehand: core rotates its own file,
	// the copy does not get the new key, so the command prints the new key, says why, and exits 0.
	b, err := os.ReadFile(h.homeCfg)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(copied, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = run(nil, "", false, "key", "rotate-management", "--yes", "--config", copied)
	if code != exitOK || fileKey() == key || out != fileKey()+"\n" {
		t.Errorf("rotate from a copy: %d %q %q (rotated: %v)", code, out, errOut, fileKey() != key)
	}
	if c, err := os.ReadFile(copied); err != nil || !bytes.Equal(c, b) {
		t.Errorf("rotate from a copy changed the copy: %v", err)
	}
	mustContain(t, "rotate from a copy", errOut, copied+" (--config) does not hold it", "core wrote it to the config file it runs on")

	// The new key cannot be written out: the command fails and names the file that holds it.
	key = fileKey()
	errOut, code = runTo(full, nil, "key", "rotate-management", "--yes", "--show-key")
	if code != exitFailed || fileKey() == key {
		t.Errorf("rotate --show-key to a full output: %d %q (rotated: %v)", code, errOut, fileKey() != key)
	}
	mustContain(t, "rotate --show-key to a full output", errOut, "could not be written to standard output", "it is in "+h.homeCfg)

	// Core rotates and its reply is lost: the new key is in core's config file, which the command
	// checks with core before naming it, and prints with --show-key.
	drop.Store(dropAfter)
	key = fileKey()
	out, errOut, code = run(nil, "", false, "key", "rotate-management", "--yes")
	if code != exitOK || out != "" || fileKey() == key {
		t.Errorf("rotate, reply lost: %d %q %q (rotated: %v)", code, out, errOut, fileKey() != key)
	}
	mustContain(t, "rotate, reply lost", errOut, "reply was lost", "Core wrote the new key to "+h.homeCfg)
	key = fileKey()
	out, errOut, code = run(nil, "", false, "key", "rotate-management", "--yes", "--show-key")
	if code != exitOK || fileKey() == key || out != fileKey()+"\n" {
		t.Errorf("rotate --show-key, reply lost: %d %q %q", code, out, errOut)
	}
	// With no config file to find it in, the command says the key was rotated and lost to it.
	key = fileKey()
	out, errOut, code = run(map[string]string{"PLUXY_URL": base, "PLUXY_MANAGEMENT_KEY": key}, "", false, "key", "rotate-management", "--yes", "--show-key")
	if code != exitFailed || out != "" || fileKey() == key {
		t.Errorf("rotate --show-key, reply lost, no config: %d %q %q", code, out, errOut)
	}
	mustContain(t, "rotate --show-key, reply lost, no config", errOut, "rotated the management key, but its reply, which held the new key, was lost", "the old key no longer works")
	// The reply is lost before core sees the call: the old key still works, and the command fails.
	drop.Store(dropBefore)
	key = fileKey()
	out, errOut, code = run(nil, "", false, "key", "rotate-management", "--yes")
	if code != exitFailed || out != "" || fileKey() != key {
		t.Errorf("rotate, call lost: %d %q %q", code, out, errOut)
	}
	mustContain(t, "rotate, call lost", errOut, "the old key still works")
}

// A rotation that core answers with no new key, an empty object or an empty body, is taken as
// one whose reply was lost: the command asks core with the old key whether it rotated, and never
// exits 0 without the key.
func TestRotateWithNoKeyInTheReply(t *testing.T) {
	const oldKey = "fake-old-management-key"
	for _, c := range []struct {
		name, reply string
		rotated     bool
		args        []string
		want        string
	}{
		{"an empty object, not rotated", "{}", false, nil, "the old key still works"},
		{"an empty body, not rotated", "", false, nil, "the old key still works"},
		{"an empty object with --json, not rotated", "{}", false, []string{"--json"}, "the old key still works"},
		{"an empty object, rotated", "{}", true, nil, "the old key no longer works"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var rotated atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+oldKey || rotated.Load() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch {
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/management-key/rotate"):
					rotated.Store(c.rotated)
					w.Write([]byte(c.reply))
				case strings.HasSuffix(r.URL.Path, "/status"):
					w.Write([]byte(`{"version":"dev"}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			h := newTestHome(t)
			emptyFolder(t)
			h.env["PLUXY_URL"] = srv.URL
			h.env["PLUXY_MANAGEMENT_KEY"] = oldKey
			var out, errOut bytes.Buffer
			args := append([]string{"key", "rotate-management", "--yes", "--show-key"}, c.args...)
			code := runCLIWithInput(args, strings.NewReader(""), &out, &errOut, h.getenv, false)
			if code != exitFailed || out.String() != "" {
				t.Errorf("%d %q %q, want exit %d and nothing on standard output", code, out.String(), errOut.String(), exitFailed)
			}
			mustContain(t, c.name, errOut.String(), c.want)
		})
	}
}

// failingWriter is an output that takes nothing, as /dev/full.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// pluxy key management says why there is no key to show: the config file that does not parse,
// the --config that is not there, each place it looked, and the desktop app's core.
func TestKeyManagementSaysWhyThereIsNoKey(t *testing.T) {
	run := func(h *testHome, args ...string) (string, string, int) {
		var out, errOut bytes.Buffer
		code := runCLIWithInput(append([]string{"key", "management"}, args...), strings.NewReader(""), &out, &errOut, h.getenv, false)
		return out.String(), errOut.String(), code
	}
	t.Run("nothing anywhere", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		out, errOut, code := run(h)
		if code != exitNotFound || out != "" {
			t.Errorf("%d %q %q", code, out, errOut)
		}
		mustContain(t, "nothing anywhere", errOut, "no config.json in this folder", h.homeCfg, "pluxy init")
	})
	t.Run("a config that does not parse", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		if err := os.MkdirAll(filepath.Dir(h.homeCfg), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.homeCfg, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, errOut, code := run(h)
		if code != exitFailed || out != "" || strings.Contains(errOut, "no config file was found") {
			t.Errorf("%d %q %q", code, out, errOut)
		}
		mustContain(t, "a config that does not parse", errOut, h.homeCfg)
	})
	t.Run("a config without a management_key", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		if err := os.MkdirAll(filepath.Dir(h.homeCfg), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(h.homeCfg, []byte(`{"listen": "127.0.0.1:18416", "client_keys": ["fake-client-key-1"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		// Core refuses such a config, and so does the command, with core's reason.
		out, errOut, code := run(h)
		if code != exitFailed || out != "" {
			t.Errorf("%d %q %q", code, out, errOut)
		}
		mustContain(t, "a config without a management_key", errOut, h.homeCfg, "no management key", "pluxy init")
	})
	t.Run("a --config that is not there", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		missing := filepath.Join(t.TempDir(), "missing.json")
		out, errOut, code := run(h, "--config", missing)
		if code != exitNotFound || out != "" {
			t.Errorf("%d %q %q", code, out, errOut)
		}
		mustContain(t, "a --config that is not there", errOut, "no config file at "+missing)
	})
	t.Run("only the desktop app's core", func(t *testing.T) {
		h := newTestHome(t)
		emptyFolder(t)
		writeConfig(t, h.appCfg, 18414, "fake-app-core-key")
		out, errOut, code := run(h)
		if code != exitNotFound || out != "" || strings.Contains(out+errOut, "fake-app-core-key") {
			t.Errorf("%d %q %q", code, out, errOut)
		}
		mustContain(t, "only the app's core", errOut, h.homeCfg, "--config "+h.appCfg, "pluxy init")
		// Its key is shown from that core's config only when asked for with --config.
		if out, errOut, code := run(h, "--config", h.appCfg, "--show-key"); code != exitOK || out != "fake-app-core-key\n" {
			t.Errorf("--config of the app's core: %d %q %q", code, out, errOut)
		}
	})
}

// The path key management names is escaped when the output is a terminal, since a folder's name
// can hold bytes that would drive the terminal.
func TestKeyManagementPathEscaped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file names cannot hold control characters")
	}
	h := newTestHome(t)
	emptyFolder(t)
	cfg := filepath.Join(t.TempDir(), foreignSample, "config.json")
	writeConfig(t, cfg, 18415, "fake-escaped-key")
	oldOut, oldErr := stdoutIsTerminal, stderrIsTerminal
	defer func() { stdoutIsTerminal, stderrIsTerminal = oldOut, oldErr }()
	stdoutIsTerminal = func(io.Writer) bool { return true }
	stderrIsTerminal = func(io.Writer) bool { return true }
	for _, args := range [][]string{{"key", "management"}, {"key", "management", "--show-key"}} {
		var out, errOut bytes.Buffer
		if code := runCLIWithInput(append(args, "--config", cfg), strings.NewReader(""), &out, &errOut, h.getenv, false); code != exitOK {
			t.Fatalf("%v: %d %q", args, code, errOut.String())
		}
		assertNoRawControls(t, "stdout", out.String())
		assertNoRawControls(t, "stderr", errOut.String())
		mustContain(t, "escaped path", out.String()+errOut.String(), foreignCellEscaped)
	}
}
