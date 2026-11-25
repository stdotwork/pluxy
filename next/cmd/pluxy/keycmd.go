package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/stdotwork/pluxy/next/internal/server"
)

// showKeyFlag adds --show-key: the management key is printed only when it is asked for.
func showKeyFlag(fs *flag.FlagSet, o *opts) {
	fs.BoolVar(&o.showKey, "show-key", false, "print the management key on standard output")
}

// runKeyManagement is pluxy key management: where the management key the pluxy commands use is
// kept, and with --show-key the key itself, alone on standard output. It calls no core.
func runKeyManagement(x *invocation) error {
	t := x.target
	if t.key == "" {
		return notFoundError{"no management key: none was given with --key or PLUXY_MANAGEMENT_KEY, and no config file was found to read one from"}
	}
	var where string
	switch t.keyFrom {
	case fromConfig:
		where = "the management_key field of " + t.configName()
	case "--key":
		where = "--key on this command line; pluxy keeps it nowhere else"
	default:
		where = "the " + t.keyFrom + " environment variable"
	}
	if x.json {
		out := map[string]string{"from": t.keyFrom}
		if t.keyFrom == fromConfig {
			out["config"] = absPath(t.config)
		}
		if x.opts.showKey {
			out["key"] = t.key
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		x.printf("%s\n", b)
		return nil
	}
	if x.opts.showKey {
		x.eprintf("The management key, from %s:\n", where)
		x.printf("%s\n", t.key)
		return nil
	}
	x.printf("The management key is in %s.\n", where)
	x.eprintf("pluxy key management --show-key prints it.\n")
	return nil
}

// runKeyRotate is pluxy key rotate-management: core makes a new management key and writes it to
// its config file, and the old one stops working at once. The new key is printed only with
// --show-key; without it, the command rotates only when it found core through a config file,
// which core then writes the new key to, so the person can still read it there.
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
	case !x.opts.yes && !confirm(x.stdin, x.stderr, fmt.Sprintf("Replace the management key of core at %s? Every program that uses the old key stops reaching core until it is given the new one. [y/N]: ", x.errCell(t.url))):
		x.eprintf("The management key was not rotated.\n")
		return nil
	}
	var r struct {
		Value string `json:"value"`
	}
	body, err := x.client.Call(x.ctx, http.MethodPost, "/management-key/rotate", nil, nil, &r)
	if err != nil {
		return err
	}
	if x.json {
		_, err = x.stdout.Write(body)
		return err
	}
	if x.opts.showKey {
		x.printf("%s\n", r.Value)
	}
	x.eprintf("The management key is rotated: the old one no longer works.\n")
	if t.keyFrom == fromConfig {
		switch cfg, err := server.LoadConfig(t.config); {
		case err != nil:
			x.eprintf("%s could not be read to check that it holds the new key: %v\n", t.configName(), err)
		case cfg.ManagementKey == r.Value && r.Value != "":
			x.eprintf("Core wrote the new key to %s; pluxy commands that read that file use it from now on.\n", t.configName())
		default:
			x.eprintf("%s does not hold the new key, so core at %s runs on another config file: the new key is in that one.\n", t.configName(), t.url)
		}
	}
	switch t.keyFrom {
	case "PLUXY_MANAGEMENT_KEY":
		x.eprintf("PLUXY_MANAGEMENT_KEY still holds the old key: set it to the new one.\n")
	case "--key":
		x.eprintf("The key given with --key was the old one: give the new one from now on.\n")
	}
	x.eprintf("Give the new key to every other program that used the old one: sign in to the web panel again; " +
		"in the desktop app, enter it on the Connection page when the app connects to this core, or quit and reopen the app when it runs this core itself; " +
		"and update PLUXY_MANAGEMENT_KEY or --key wherever scripts set them.\n")
	return nil
}

// absPath is path made absolute, or path when that fails.
func absPath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
