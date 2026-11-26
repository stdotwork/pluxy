package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/stdotwork/pluxy/next/internal/server"
	"github.com/stdotwork/pluxy/next/management"
)

// showKeyFlag adds --show-key: the management key is printed only when it is asked for.
func showKeyFlag(fs *flag.FlagSet, o *opts) {
	fs.BoolVar(&o.showKey, "show-key", false, "print the management key on standard output")
}

// emit writes to standard output and returns the writer's error, for output a command must
// not report as delivered when it was not: a key.
func (x *invocation) emit(format string, a ...any) error {
	_, err := fmt.Fprintf(x.stdout, format, a...)
	return err
}

// runKeyManagement is pluxy key management: where the management key the pluxy commands use is
// kept, and with --show-key the key itself, alone on standard output. It calls no core. When
// there is no key to find, findCore has already said why: each place it looked, or the config
// file it could not read, which includes one without a management_key (server.LoadConfig
// refuses it).
func runKeyManagement(x *invocation) error {
	t := x.target
	var where string
	switch t.keyFrom {
	case fromConfig:
		where = "the management_key field of " + t.configName()
	case "--key":
		where = "--key on this command line; pluxy keeps it nowhere else"
	default:
		where = "the " + t.keyFrom + " environment variable"
	}
	var err error
	switch {
	case x.json:
		out := map[string]string{"from": t.keyFrom}
		if t.keyFrom == fromConfig {
			out["config"] = absPath(t.config)
		}
		if x.opts.showKey {
			out["key"] = t.key
		}
		b, merr := json.Marshal(out)
		if merr != nil {
			return merr
		}
		err = x.emit("%s\n", b)
	case x.opts.showKey:
		x.eprintf("The management key, from %s:\n", x.errCell(where))
		err = x.emit("%s\n", t.key)
	default:
		err = x.emit("The management key is in %s.\n", x.cell(where))
		x.eprintf("pluxy key management --show-key prints it.\n")
	}
	if err != nil {
		return fmt.Errorf("writing to standard output failed: %v", err)
	}
	return nil
}

// runKeyRotate is pluxy key rotate-management: core makes a new management key and writes it to
// its config file, and the old one stops working at once. That cannot be undone, so the command
// first makes sure the person can get the new key afterwards: from the config file it read, when
// core's own config file has the same contents (GET /config's hash), so core will write the new
// key there; else only from standard output, with --show-key. After the rotation it checks that
// the file holds the new key, and prints the key when it does not: a file with core's file's
// contents need not be core's file. When core's reply is lost, or holds no key, it finds out from
// core and the file whether the key was rotated and where the new one is. It exits 0 only when
// the new key was delivered or is in the file.
func runKeyRotate(x *invocation) error {
	t := x.target
	viaConfig := t.urlFrom == fromConfig && t.keyFrom == fromConfig
	switch {
	case !x.opts.showKey && !viaConfig:
		return usageError{"this command did not find core through a config file, so it cannot tell where core keeps the new key: add --show-key to print the new key once rotated"}
	case !x.opts.showKey && x.json:
		return usageError{"--json prints the API's reply, which holds the new key: add --show-key"}
	case !x.opts.yes && !x.prompts():
		return usageError{"rotating the management key stops every program that uses the old one: pass --yes, or run it on a terminal to be asked"}
	}
	// inFile: the config file read has core's config file's contents, so core writes the new key
	// to it. A file with other contents is another file, and core would write the new key elsewhere.
	inFile := false
	if t.config != "" {
		var cur management.Config
		if _, err := x.client.Call(x.ctx, http.MethodGet, "/config", nil, nil, &cur); err != nil {
			return err
		}
		b, err := os.ReadFile(t.config)
		inFile = err == nil && cur.Hash == contentHash(b)
		if !inFile && !x.opts.showKey {
			why := "its own config file has other contents, so the new key would not be written to this one"
			if err != nil {
				why = fmt.Sprintf("this file could not be read (%v)", err)
			}
			return usageError{fmt.Sprintf("core at %s does not run on %s: %s. "+
				"Pass --config with the file core was started with, or add --show-key to print the new key once rotated", t.url, t.configName(), why)}
		}
	}
	if !x.opts.yes && !confirm(x.stdin, x.stderr, fmt.Sprintf("Replace the management key of core at %s? Every program that uses the old key stops reaching core until it is given the new one. [y/N]: ", x.errCell(t.url))) {
		x.eprintf("The management key was not rotated.\n")
		return nil
	}
	var r struct {
		Value string `json:"value"`
	}
	body, err := x.client.Call(x.ctx, http.MethodPost, "/management-key/rotate", nil, nil, &r)
	var ae *management.APIError
	switch {
	case errors.As(err, &ae):
		return err // core refused, and changed nothing
	case err != nil:
		return x.rotateUnanswered(err, inFile)
	case r.Value == "":
		return x.rotateUnanswered(errors.New("its reply held no new key"), inFile)
	}
	// held: the config file read holds the new key. When it does not, the key is printed even
	// without --show-key, since no file this command knows holds it.
	held := inFile && fileKey(t.config) == r.Value
	var werr error
	switch {
	case x.json:
		_, werr = x.stdout.Write(body)
	case x.opts.showKey || !held:
		werr = x.emit("%s\n", r.Value)
	}
	x.eprintf("The management key is rotated: the old one no longer works.\n")
	switch {
	case held:
		x.eprintf("Core wrote the new key to %s; pluxy commands that read that file use it from now on.\n", x.errCell(t.configName()))
	case !x.opts.showKey:
		x.eprintf("The new key is printed on standard output because %s does not hold it: core wrote it to the config file it runs on, which had the same contents.\n", x.errCell(t.configName()))
	case t.config != "":
		x.eprintf("%s does not hold the new key: core at %s runs on another config file, which does.\n", x.errCell(t.configName()), x.errCell(t.url))
	}
	x.giveNewKey()
	switch {
	case werr != nil && held:
		return fmt.Errorf("the new key could not be written to standard output (%v); it is in %s", werr, t.configName())
	case werr != nil:
		return fmt.Errorf("the new key could not be written to standard output (%v), and this command knows no file that holds it: it is in the config file core at %s runs on", werr, t.url)
	}
	return nil
}

// rotateUnanswered is the end of a rotation whose reply did not arrive or held no key (callErr):
// core may have made the new key and written it to its config file before the reply was lost.
// The old key says whether it did; the config file read, when core runs on it, holds the new key, which is
// checked with core before it is named or, with --show-key, printed.
func (x *invocation) rotateUnanswered(callErr error, inFile bool) error {
	t := x.target
	_, err := x.client.Call(x.ctx, http.MethodGet, "/status", nil, nil, nil)
	var ae *management.APIError
	switch {
	case err == nil:
		return fmt.Errorf("core at %s did not answer the rotation (%v), and the old key still works: the key was not rotated, or not yet. Run pluxy key management or pluxy status to check", t.url, callErr)
	case !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized:
		where := "the config file core runs on"
		if inFile {
			where = t.configName() + ", if core rotated it"
		}
		return fmt.Errorf("core at %s did not answer the rotation (%v) and cannot be asked whether it rotated the key (%v). If the old key no longer works, the new one is in %s", t.url, callErr, err, where)
	}
	// The old key is refused: the key was rotated.
	if inFile {
		key := fileKey(t.config)
		check := management.Client{URL: t.url, Key: key, HTTP: t.http}
		var cur management.Config
		b, rerr := os.ReadFile(t.config)
		if _, cerr := check.Call(x.ctx, http.MethodGet, "/config", nil, nil, &cur); key != "" && cerr == nil && rerr == nil && cur.Hash == contentHash(b) {
			x.eprintf("Core rotated the management key, but its reply was lost (%v); the old key no longer works.\n", callErr)
			x.eprintf("Core wrote the new key to %s; pluxy commands that read that file use it from now on.\n", x.errCell(t.configName()))
			var werr error
			if x.opts.showKey {
				if x.json {
					var j []byte
					if j, werr = json.Marshal(map[string]string{"value": key}); werr == nil {
						werr = x.emit("%s\n", j)
					}
				} else {
					werr = x.emit("%s\n", key)
				}
			}
			x.giveNewKey()
			if werr != nil {
				return fmt.Errorf("the new key could not be written to standard output (%v); it is in %s", werr, t.configName())
			}
			return nil
		}
	}
	x.giveNewKey()
	var known string
	switch {
	case t.config == "":
		known = "this command read no config file"
	case !inFile:
		known = t.configName() + " is another file"
	default:
		known = t.configName() + " did not hold a key core accepts when this command checked it"
	}
	return fmt.Errorf("core at %s rotated the management key, but its reply, which held the new key, was lost (%v), and the old key no longer works. The new key is in the config file core runs on; %s", t.url, callErr, known)
}

// giveNewKey says, after a rotation, who must be given the new key.
func (x *invocation) giveNewKey() {
	switch x.target.keyFrom {
	case "PLUXY_MANAGEMENT_KEY":
		x.eprintf("PLUXY_MANAGEMENT_KEY still holds the old key: set it to the new one.\n")
	case "--key":
		x.eprintf("The key given with --key was the old one: give the new one from now on.\n")
	}
	x.eprintf("Give the new key to every other program that used the old one: sign in to the web panel again; " +
		"in the desktop app, enter it on the Connection page when the app connects to this core, or quit and reopen the app when it runs this core itself; " +
		"and update PLUXY_MANAGEMENT_KEY or --key wherever scripts set them.\n")
}

// contentHash is a config file's hash as GET /config shows it.
func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// fileKey is the management key in the config file at path, "" when it cannot be read.
func fileKey(path string) string {
	cfg, err := server.LoadConfig(path)
	if err != nil {
		return ""
	}
	return cfg.ManagementKey
}

// absPath is path made absolute, or path when that fails.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
