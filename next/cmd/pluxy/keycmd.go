package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

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

// runKeyRotate is pluxy key rotate-management: pluxy changes its management key in two steps,
// so a lost reply never leaves the person without a key that works. Stage makes a new key that
// pluxy keeps in memory only, and names the config file pluxy runs on; the old key still works.
// The command then makes sure the person can get the new key: on standard output with
// --show-key, where a failed write ends it, else in the config file it read, when that is the
// file pluxy names (os.SameFile: a copy with the same contents is another file). Only then does
// commit put the key into use: pluxy writes it to its config file, and the old key stops working.
// Pluxy decides a commit under its config-write lock and answers 204 for the key already in use,
// so after a lost commit reply one more commit with the old key tells whether the key changed.
func runKeyRotate(x *invocation) error {
	t := x.target
	viaConfig := t.urlFrom == fromConfig && t.keyFrom == fromConfig
	switch {
	case !x.opts.showKey && !viaConfig:
		return usageError{"this command did not find pluxy through a config file, so it cannot tell where pluxy keeps the new key: add --show-key to print the new key"}
	case !x.opts.showKey && x.json:
		return usageError{"--json prints the API's reply, which holds the new key: add --show-key"}
	case !x.opts.yes && !x.prompts():
		return usageError{"changing the management key stops every program that uses the old one: pass --yes, or run it on a terminal to be asked"}
	}
	if !x.opts.yes && !confirm(x.stdin, x.stderr, fmt.Sprintf("Replace the management key of pluxy at %s? Every program that uses the old key stops reaching pluxy until it is given the new one. [y/N]: ", x.errCell(t.url))) {
		x.eprintf("The management key was not changed.\n")
		return nil
	}
	var staged struct {
		Value  string `json:"value"`
		Config string `json:"config"`
	}
	body, err := x.client.Call(x.ctx, http.MethodPost, "/management-key/rotate/stage", nil, nil, &staged)
	var ae *management.APIError
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusNotFound:
		return fmt.Errorf("%s is an older pluxy that cannot change its key in two steps. Nothing changed. Use the pluxy command of the same version as it", t.url)
	case errors.As(err, &ae):
		return err // pluxy refused, and changed nothing
	case err == nil && (staged.Value == "" || staged.Config == ""):
		err = errors.New("its reply held no new key")
	}
	if err != nil {
		return fmt.Errorf("%s did not answer the key change (%v). Nothing changed: your management key still works. Run the command again", t.url, err)
	}
	inFile := t.config != "" && sameFile(t.config, staged.Config)
	if !x.opts.showKey && !inFile {
		return usageError{fmt.Sprintf("%s runs on %s, not on %s, so the new key would not be written where this command looks. Nothing changed. Pass --config %s, or add --show-key to print the new key",
			t.url, staged.Config, t.configName(), staged.Config)}
	}
	if x.opts.showKey {
		var werr error
		if x.json {
			_, werr = x.stdout.Write(body)
		} else {
			werr = x.emit("%s\n", staged.Value)
		}
		if werr != nil {
			return fmt.Errorf("the new key could not be printed (%v), so it was not put into use. Nothing changed: your management key still works", werr)
		}
	}
	commit := map[string]string{"value": staged.Value}
	_, err = x.client.Call(x.ctx, http.MethodPost, "/management-key/rotate/commit", nil, commit, nil)
	switch {
	case errors.As(err, &ae) && ae.Status == http.StatusConflict:
		return x.notInUse(ae)
	case errors.As(err, &ae):
		return err
	case err != nil:
		if err := x.commitAgain(commit, staged.Config); err != nil {
			return err
		}
	}
	x.eprintf("The management key is changed: the old one no longer works.\n")
	x.eprintf("pluxy at %s wrote the new key to %s, the config file it runs on.\n", x.errCell(t.url), x.errCell(staged.Config))
	switch {
	case inFile:
		x.eprintf("pluxy commands that read %s use it from now on.\n", x.errCell(t.configName()))
	case t.config != "":
		x.eprintf("%s does not hold the new key: it is another file.\n", x.errCell(t.configName()))
	}
	x.giveNewKey()
	return nil
}

// notInUse is the end of a key change whose commit pluxy answered 409: the staged key was not
// put into use, and the old key still works.
func (x *invocation) notInUse(err error) error {
	return fmt.Errorf("%s did not put the new key into use (%v). Nothing changed: your management key still works. Run the command again", x.target.url, err)
}

// commitAgain sends commit once more, with the old key, after the first commit's reply was
// lost: pluxy ran the first one, if at all, before this one. 204: this commit put the key into
// use, or found it in use. 401: the old key no longer works, so the first one did, which the new
// key's answer confirms. 409: neither put it into use. config is the file pluxy runs on.
func (x *invocation) commitAgain(commit map[string]string, config string) error {
	t := x.target
	_, err := x.client.Call(x.ctx, http.MethodPost, "/management-key/rotate/commit", nil, commit, nil)
	var ae *management.APIError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae) && ae.Status == http.StatusConflict:
		return x.notInUse(ae)
	case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
		check := management.Client{URL: t.url, Key: commit["value"], HTTP: t.http}
		if _, err = check.Call(x.ctx, http.MethodGet, "/status", nil, nil, nil); err == nil {
			return nil
		}
		// The old key is refused, so the new one is the key unless something else changed it.
		if x.opts.showKey {
			return fmt.Errorf("%s refuses the old key now, but did not answer the new one (%v). Try the new key printed above", t.url, err)
		}
		return fmt.Errorf("%s refuses the old key now, but did not answer the new one (%v). Use the key in %s", t.url, err, config)
	}
	if x.opts.showKey {
		return fmt.Errorf("%s stopped answering while changing the key (%v). Try the new key printed above; if it is refused, your old key still works", t.url, err)
	}
	return fmt.Errorf("%s stopped answering while changing the key (%v). Use the key in %s: it holds whichever key works", t.url, err, config)
}

// giveNewKey says, after a key change, who must be given the new key.
func (x *invocation) giveNewKey() {
	switch x.target.keyFrom {
	case "PLUXY_MANAGEMENT_KEY":
		x.eprintf("PLUXY_MANAGEMENT_KEY still holds the old key: set it to the new one.\n")
	case "--key":
		x.eprintf("The key given with --key was the old one: give the new one from now on.\n")
	}
	x.eprintf("Give the new key to every other program that used the old one: sign in to the web panel again; " +
		"in the desktop app, enter it on the Connection page when the app connects to this pluxy, or quit and reopen the app when it runs this pluxy itself; " +
		"and update PLUXY_MANAGEMENT_KEY or --key wherever scripts set them.\n")
}

// sameFile reports whether the paths a and b name the same file.
func sameFile(a, b string) bool {
	fa, err := os.Stat(a)
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}

// absPath is path made absolute, or path when that fails.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
