package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode/utf16"

	"github.com/stdotwork/pluxy/next/internal/buildinfo"
	"github.com/stdotwork/pluxy/next/internal/host"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

// Exit codes of the client subcommands (next/management/README.md).
const (
	exitOK          = 0
	exitFailed      = 1
	exitUsage       = 2
	exitUnreachable = 3
	exitRefused     = 4
	exitNotFound    = 5
	exitChange      = 6
	exitWaiting     = 7
)

// command is one client subcommand: the words that name it, its arguments, the management
// calls it makes (as the API's patterns, which the route table test checks) and what it does.
type command struct {
	words string
	args  []string
	// optional: the last argument may be left out.
	optional bool
	// more: the last argument may be given more than once (at least once).
	more    bool
	routes  []string
	summary string
	// readOnly: the command changes nothing though its route is not a GET (a command whose routes
	// are all GETs needs no mark, see changesNothing), so a 409 or 422 did not refuse a change
	// (an account test the extension cannot run refused nothing).
	readOnly bool
	// flags adds the command's own flags.
	flags func(fs *flag.FlagSet, o *opts)
	run   func(x *invocation) error
}

// opts are the values of the commands' own flags.
type opts struct {
	model, version, registry                       string
	since, until, by, account, provider, clientKey string
	after, before, failed                          string
	limit                                          int
	yes                                            bool
	picks                                          stringSliceFlag
	keyID                                          string
	id                                             string
	authDir                                        string
	dryRun                                         bool
	// method, input and noWait are account add's and account login's: the sign-in method, what
	// the person entered, and whether to stop once the sign-in has started.
	method string
	input  inputFlag
	noWait bool
}

// invocation is one run of a command.
type invocation struct {
	ctx    context.Context
	client *management.Client
	args   []string
	opts   opts
	json   bool
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	tty    bool
	// restoreEcho puts the terminal's echo back on. A command that gives up while a secret is
	// being typed calls it before cancelling the sign-in in core, since that call may take
	// seconds when core does not answer.
	restoreEcho func()
}

// call makes one management call. With --json the reply's body goes to standard output as it
// came; otherwise out is decoded and show prints it.
func (x *invocation) call(method, path string, q url.Values, in, out any, show func()) error {
	if x.json {
		// The body is printed unchanged, so it is not read through the typed reply first: a
		// field a newer core changed the type of would abort the output. What does decode is
		// still decoded, for the commands whose exit code depends on it (setup apply's failed
		// picks).
		body, err := x.client.Call(x.ctx, method, path, q, in, nil)
		if err != nil {
			return err
		}
		if out != nil {
			_ = json.Unmarshal(body, out)
		}
		_, err = x.stdout.Write(body)
		return err
	}
	if _, err := x.client.Call(x.ctx, method, path, q, in, out); err != nil {
		return err
	}
	if show != nil {
		show()
	}
	return nil
}

// table writes rows as aligned columns, the first row a header.
func (x *invocation) table(rows [][]string) {
	tw := tabwriter.NewWriter(x.stdout, 0, 0, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

func (x *invocation) printf(format string, a ...any) { fmt.Fprintf(x.stdout, format, a...) }

// eprintf writes what a command asks a person, and its notes, on standard error: standard
// output stays the answers.
func (x *invocation) eprintf(format string, a ...any) { fmt.Fprintf(x.stderr, format, a...) }

// prompts reports whether a command may ask the person for a value: only on a terminal, and never
// with --json, whose standard output is the API's reply and nothing else.
func (x *invocation) prompts() bool { return x.tty && !x.json }

// asks refuses a value that would be read from a terminal with --json: nothing is shown there, so
// a read for a line would look like a hang, and standard output belongs to the reply.
func (x *invocation) asks(f protocol.LoginInput) error {
	if x.json && x.tty {
		return usageError{"with --json nothing is asked on a terminal: " + inputHint(f)}
	}
	return nil
}

// terminal is standard input when it is a terminal, whose echo a secret value is kept from.
func (x *invocation) terminal() *os.File {
	if !x.tty {
		return nil
	}
	if f, ok := x.stdin.(*os.File); ok {
		return f
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func accountPath(id string) string { return "/accounts/" + url.PathEscape(id) }

func init() {
	commands = append(commands, setupCommands()...)
}

// commands are the client subcommands, in the order help lists them.
var commands = []command{
	{words: "status", routes: []string{"GET /status"}, summary: "core's state: config versions, extensions, build",
		run: func(x *invocation) error {
			var s management.Status
			return x.call(http.MethodGet, "/status", nil, nil, &s, func() { printStatus(x, s) })
		}},
	{words: "reload", routes: []string{"POST /reload"}, summary: "reload the config file now",
		run: func(x *invocation) error {
			var s management.Status
			err := x.call(http.MethodPost, "/reload", nil, nil, &s, func() { x.printf("config revision %d is active\n", s.Active.Revision) })
			var ae *management.APIError
			if errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity && json.Unmarshal(ae.Body, &s) == nil && s.Desired.Error != "" {
				ae.Message = "core refused the config: " + s.Desired.Error
			}
			return err
		}},
	{words: "config show", routes: []string{"GET /config"}, summary: "the config file, with its secrets replaced by placeholders",
		run: func(x *invocation) error {
			var c management.Config
			return x.call(http.MethodGet, "/config", nil, nil, &c, func() {
				x.printf("%s", c.Content)
				if !strings.HasSuffix(c.Content, "\n") {
					x.printf("\n")
				}
			})
		}},
	{words: "settings", routes: []string{"GET /settings"}, summary: "the basic and routing settings by name",
		run: func(x *invocation) error {
			var s management.Settings
			return x.call(http.MethodGet, "/settings", nil, nil, &s, func() { printFlat(x, s) })
		}},
	{words: "key list", routes: []string{"GET /client-keys"}, summary: "the client keys, masked",
		run: func(x *invocation) error {
			var r management.ClientKeysReply
			return x.call(http.MethodGet, "/client-keys", nil, nil, &r, func() {
				rows := [][]string{{"ID", "KEY"}}
				for _, k := range r.Keys {
					rows = append(rows, []string{k.ID, k.Masked})
				}
				x.table(rows)
			})
		}},
	{words: "key add", routes: []string{"POST /client-keys"}, summary: "add a new client key and print it once",
		run: func(x *invocation) error {
			var r management.NewClientKeyReply
			return x.call(http.MethodPost, "/client-keys", nil, nil, &r, func() {
				x.printf("%s\n", r.Value)
				x.eprintf("Save this key now: pluxy will not print it again.\n")
			})
		}},
	{words: "key remove", args: []string{"id"}, routes: []string{"DELETE /client-keys/{id}"}, summary: "remove a client key",
		run: func(x *invocation) error {
			return x.call(http.MethodDelete, "/client-keys/"+url.PathEscape(x.args[0]), nil, nil, nil, func() {
				x.printf("client key %s removed\n", x.args[0])
			})
		}},
	{words: "logs", routes: []string{"GET /logs"}, summary: "lines of core's log files",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.after, "after", "", "only lines after this time (Unix seconds, as a reply's latest-timestamp)")
			fs.IntVar(&o.limit, "limit", 0, "at most this many lines (default: core's, 1000)")
		},
		run: func(x *invocation) error {
			q := url.Values{}
			if x.opts.after != "" {
				q.Set("after", x.opts.after)
			}
			if x.opts.limit > 0 {
				q.Set("limit", strconv.Itoa(x.opts.limit))
			}
			var l management.LogLines
			return x.call(http.MethodGet, "/logs", q, nil, &l, func() {
				for _, line := range l.Lines {
					x.printf("%s\n", line)
				}
			})
		}},
	logsErrorsCommand, logsRequestCommand, logsClearCommand,
	{words: "ext list", routes: []string{"GET /packages"}, summary: "installed extension packages and their state",
		run: func(x *invocation) error {
			var r management.PackagesReply
			return x.call(http.MethodGet, "/packages", nil, nil, &r, func() {
				rows := [][]string{{"ID", "VERSION", "ENABLED", "STATE", "REGISTRY"}}
				for _, p := range r.Packages {
					state := p.State
					if p.Error != "" {
						state += ": " + p.Error
					}
					rows = append(rows, []string{p.ID, p.Version, yesNo(p.Enabled), state, dash(p.Registry)})
				}
				x.table(rows)
			})
		}},
	{words: "ext available", routes: []string{"GET /registries"}, summary: "what each registry offers",
		run: func(x *invocation) error {
			var r management.RegistriesReply
			return x.call(http.MethodGet, "/registries", nil, nil, &r, func() {
				rows := [][]string{{"REGISTRY", "PACKAGE", "TITLE", "KIND", "TIER", "VERSIONS"}}
				for _, reg := range r.Registries {
					if reg.Error != "" {
						rows = append(rows, []string{reg.Name, "-", "-", "-", "-", "error: " + reg.Error})
					}
					for _, p := range reg.Packages {
						var versions []string
						for _, rel := range p.Releases {
							versions = append(versions, rel.Version)
						}
						rows = append(rows, []string{reg.Name, p.ID, dash(p.Title), dash(p.Kind), dash(p.Tier), strings.Join(versions, ", ")})
					}
				}
				x.table(rows)
			})
		}},
	{words: "ext install", args: []string{"id"}, routes: []string{"POST /packages"}, summary: "install a package and enable it",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.version, "version", "", "the release to install (default: the newest this core can run)")
			fs.StringVar(&o.registry, "registry", "", "the registry to install from (default: the first that has it)")
		},
		run: func(x *invocation) error {
			var r management.PackageReply
			req := management.InstallRequest{ID: x.args[0], Version: x.opts.version, Registry: x.opts.registry, Enable: true}
			return x.call(http.MethodPost, "/packages", nil, req, &r, func() {
				x.printf("%s %s installed: %s\n", r.Package.ID, r.Package.Version, r.Package.State)
			})
		}},
	packageVerb("enable", "start routing to an installed package"),
	packageVerb("disable", "stop routing to an installed package"),
	{words: "ext remove", args: []string{"id"}, routes: []string{"DELETE /packages/{publisher}/{name}"}, summary: "remove a package's files; its accounts stay",
		run: func(x *invocation) error {
			return x.call(http.MethodDelete, management.PackagePath(x.args[0]), nil, nil, nil, func() {
				x.printf("%s removed\n", x.args[0])
			})
		}},
	{words: "account methods", routes: []string{"GET /login-methods"}, summary: "how each running extension can add an account: its sign-in methods and what each asks for",
		run: runAccountMethods},
	{words: "account add", args: []string{"provider"}, routes: []string{"POST /accounts", "GET /logins/{id}", "POST /logins/{id}/input", "DELETE /logins/{id}"},
		summary: "add an account by signing in, following the sign-in to its end and asking for what it needs",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.id, "id", "", "the account's ID (default: a new one, account- and 12 hex digits)")
			signInFlags(fs, o)
		},
		run: runAccountAdd},
	{words: "account login", args: []string{"id"}, routes: []string{"POST /accounts/{id}/login"},
		summary: "sign in to an account again, following the sign-in as account add does",
		flags:   signInFlags,
		run:     runAccountLogin},
	{words: "account remove", args: []string{"id"}, routes: []string{"DELETE /accounts/{id}"}, summary: "remove an account and its credential",
		run: runAccountRemove},
	{words: "account refresh", args: []string{"id"}, routes: []string{"POST /accounts/{id}/refresh"}, summary: "renew an account's credential now",
		run: runAccountRefresh},
	{words: "account set", args: []string{"id", "name=value"}, more: true, routes: []string{"PATCH /accounts/{id}"},
		summary: "change an account: disabled, priority, weight, disable_cooling, max_concurrency, request_retry, prefix, models, excluded_models, model_alias, proxy_url, headers, timeouts or settings (null sets one back)",
		run:     runAccountSet},
	{words: "account import", args: []string{"file"}, routes: []string{"POST /accounts/import", "POST /vertex/import"},
		summary: "add an account from a credential file: a Google service account key (a JSON object with a private_key and a project_id) is imported as a Vertex account, any other file as today's core's credential file (- reads a service account key from standard input)",
		run:     runAccountImport},
	{words: "account list", routes: []string{"GET /accounts"}, summary: "every account and its state",
		run: func(x *invocation) error {
			var r management.AccountsReply
			return x.call(http.MethodGet, "/accounts", nil, nil, &r, func() {
				rows := [][]string{{"ID", "PUBLIC ID", "LABEL", "EXTENSION", "SOURCE", "HEALTH", "UNTIL", "WHY"}}
				for _, a := range r.Accounts {
					health := dash(a.Health.State)
					if a.Health.Source == management.SourceUnverified {
						health += " (unverified)"
					}
					until := "-"
					if a.Health.Until != nil {
						until = a.Health.Until.Local().Format(time.DateTime)
					}
					rows = append(rows, []string{a.ID, a.PublicID, dash(a.Label), dash(a.Extension), a.Source, health, until, dash(a.Health.Why)})
				}
				x.table(rows)
			})
		}},
	{words: "route", args: []string{"model"}, routes: []string{"GET /route"}, summary: "the accounts a request for a model would try now, in order, and those it would leave out",
		run: func(x *invocation) error {
			var r management.RouteReply
			return x.call(http.MethodGet, "/route", url.Values{"model": {x.args[0]}}, nil, &r, func() {
				chain := len(r.Chain) > 0
				if chain {
					x.printf("chain %s: %s\n\n", r.Model, chainText(r.Chain))
				}
				// With a chain, each account's step leads its row.
				row := func(step string, cells ...string) []string {
					if !chain {
						return cells
					}
					return append([]string{step}, cells...)
				}
				if len(r.Order) == 0 {
					x.printf("no account would be tried for %s\n", r.Model)
				} else {
					// Under soonest-reset each account also shows the reset it was ranked by.
					ranked := false
					for _, s := range r.Order {
						ranked = ranked || s.Rank != nil
					}
					head := row("STEP", "TRY", "PUBLIC ID", "ID", "PROVIDER", "MODEL")
					if ranked {
						head = append(head, "RESET")
					}
					rows := [][]string{head}
					for i, s := range r.Order {
						cells := row(strconv.Itoa(s.Step), strconv.Itoa(i+1), s.Account, s.ID, s.Provider, s.Model)
						if ranked {
							reset := "none known"
							switch k := s.Rank; {
							case k == nil:
							case k.FullUntil != nil:
								reset = "used up until " + k.FullUntil.Local().Format(time.DateTime)
							case k.Reset != nil:
								reset = k.Reset.Local().Format("2006-01-02 15:04")
								if k.WindowSeconds > 0 {
									reset += " (" + windowText(k.WindowSeconds) + " window)"
								}
							}
							cells = append(cells, reset)
						}
						rows = append(rows, cells)
					}
					x.table(rows)
				}
				if len(r.LeftOut) == 0 {
					return
				}
				x.printf("\nleft out:\n")
				rows := [][]string{row("STEP", "PUBLIC ID", "ID", "STATE", "UNTIL")}
				for _, l := range r.LeftOut {
					until := "-"
					if l.Until != nil {
						until = l.Until.Local().Format(time.DateTime)
					}
					rows = append(rows, row(strconv.Itoa(l.Step), l.Account, l.ID, l.State, until))
				}
				x.table(rows)
			})
		}},
	{words: "chain list", routes: []string{"GET /chains"}, summary: "the fallback chains",
		run: func(x *invocation) error {
			var r management.ChainsReply
			return x.call(http.MethodGet, "/chains", nil, nil, &r, func() {
				if len(r.Chains) == 0 {
					x.printf("no chains: each model is served by the accounts of one provider\n")
					return
				}
				rows := [][]string{{"NAME", "STEPS"}}
				for _, c := range r.Chains {
					rows = append(rows, []string{c.Name, chainText(c.Steps)})
				}
				x.table(rows)
			})
		}},
	{words: "chain set", args: []string{"name", "step"}, more: true, routes: []string{"PUT /chains/{name}"},
		summary: "set a model name's fallback chain; a step is model[@provider][:account,account] (a model with a colon in its name ends with @: llama3:8b@; what follows the last @ is a provider only when it is an extension ID, so claude-opus-4-7@20260416 is a model)",
		run: func(x *invocation) error {
			var steps []management.ChainStep
			for _, a := range x.args[1:] {
				s, err := parseStep(a)
				if err != nil {
					return err
				}
				steps = append(steps, s)
			}
			var r management.Chain
			return x.call(http.MethodPut, management.ChainPath(x.args[0]), nil, management.ChainRequest{Steps: steps}, &r, func() {
				x.printf("chain %s: %s\n", r.Name, chainText(r.Steps))
			})
		}},
	{words: "chain remove", args: []string{"name"}, routes: []string{"DELETE /chains/{name}"}, summary: "remove a fallback chain: the name goes back to one provider's accounts",
		run: func(x *invocation) error {
			return x.call(http.MethodDelete, management.ChainPath(x.args[0]), nil, nil, nil, func() {
				x.printf("chain %s removed\n", x.args[0])
			})
		}},
	accountSwitch("enable", false, "turn an account on"),
	accountSwitch("disable", true, "turn an account off"),
	{words: "account clear", args: []string{"id"}, routes: []string{"POST /accounts/{id}/cooldown/reset"}, summary: "forget what routing holds against an account",
		run: func(x *invocation) error {
			var r management.CooldownResetReply
			return x.call(http.MethodPost, accountPath(x.args[0])+"/cooldown/reset", nil, nil, &r, func() {
				x.printf("account %s: %d cooldowns cleared\n", x.args[0], r.Cleared)
			})
		}},
	{words: "account test", args: []string{"id"}, routes: []string{"POST /accounts/{id}/test"}, summary: "send one small request through an account", readOnly: true,
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.model, "model", "", "the model to ask (default: one the account serves)")
		},
		run: func(x *invocation) error {
			var r management.TestReply
			err := x.call(http.MethodPost, accountPath(x.args[0])+"/test", nil, management.TestRequest{Model: x.opts.model}, &r, func() {
				res := r.Result
				if res.OK {
					x.printf("ok: %s answered in %d ms: %q\n", cmpOr(res.Served, res.Model), res.LatencyMS, res.Reply)
				}
			})
			if err == nil && !r.Result.OK {
				msg := "the test request failed"
				if r.Result.Error != nil {
					msg += ": " + r.Result.Error.Message
				}
				return errors.New(msg)
			}
			return err
		}},
	{words: "account quota", args: []string{"id"}, routes: []string{"GET /accounts/{id}/quota"}, summary: "an account's usage windows, as its provider reports them",
		run: runAccountQuota},
	{words: "account models", args: []string{"id"}, routes: []string{"GET /accounts/{id}/models"}, summary: "the models an account serves",
		run: func(x *invocation) error {
			var r management.AccountModelsReply
			return x.call(http.MethodGet, accountPath(x.args[0])+"/models", nil, nil, &r, func() {
				if r.From != "" {
					x.printf("from: %s\n", r.From)
				}
				rows := [][]string{{"ID", "NAMES", "OWNED BY", "CONTEXT", "MAX OUTPUT", "EXCLUDED"}}
				for _, m := range r.Models {
					names := strings.Join(m.Names, ",")
					if names == "" {
						names = "-"
					}
					ctxLen := m.ContextLength
					if ctxLen == 0 {
						ctxLen = m.InputTokenLimit
					}
					maxOut := m.MaxCompletionTokens
					if maxOut == 0 {
						maxOut = m.OutputTokenLimit
					}
					ctxStr, outStr := "-", "-"
					if ctxLen > 0 {
						ctxStr = strconv.Itoa(ctxLen)
					}
					if maxOut > 0 {
						outStr = strconv.Itoa(maxOut)
					}
					rows = append(rows, []string{m.ID, names, dash(m.OwnedBy), ctxStr, outStr, yesNo(m.Excluded)})
				}
				x.table(rows)
			})
		}},
	{words: "model list", routes: []string{"GET /models"}, summary: "every model clients can ask for, and the accounts that serve it",
		run: func(x *invocation) error {
			var r management.ModelsReply
			return x.call(http.MethodGet, "/models", nil, nil, &r, func() {
				rows := [][]string{{"MODEL", "EXTENSION", "UPSTREAM", "CONTEXT", "MAX OUTPUT", "CAPABILITIES", "FROM", "ACCOUNTS"}}
				for _, m := range r.Models {
					ctxStr, outStr := "-", "-"
					if m.ContextLength > 0 {
						ctxStr = strconv.Itoa(m.ContextLength)
					}
					maxOut := m.MaxOutputTokens
					if maxOut == 0 {
						maxOut = m.MaxCompletionTokens
					}
					if maxOut > 0 {
						outStr = strconv.Itoa(maxOut)
					}
					rows = append(rows, []string{m.ID, dash(m.Extension), dash(m.Upstream), ctxStr, outStr, formatCapabilities(m.Capabilities), dash(m.From), strings.Join(m.Accounts, ",")})
				}
				x.table(rows)
			})
		}},
	{words: "model refresh", routes: []string{"POST /models/refresh"}, summary: "ask providers for their model lists again now",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.account, "account", "", "refresh only this account (default: every account)")
		},
		run: func(x *invocation) error {
			var in any
			if x.opts.account != "" {
				in = management.RefreshModelsRequest{Account: x.opts.account}
			}
			var r management.RefreshModelsReply
			return x.call(http.MethodPost, "/models/refresh", nil, in, &r, func() {
				if len(r.Accounts) == 0 {
					x.printf("no accounts to refresh (%d models served)\n", len(r.Models))
					return
				}
				rows := [][]string{{"ACCOUNT", "MODELS", "STATUS"}}
				for _, a := range r.Accounts {
					status := "ok"
					if a.Error != "" {
						if a.Kept {
							status = "kept previous list: " + a.Error
						} else {
							status = "failed: " + a.Error
						}
					}
					rows = append(rows, []string{a.Account, strconv.Itoa(a.Models), status})
				}
				x.table(rows)
			})
		}},
	{words: "usage", routes: []string{"GET /usage"}, summary: "request and token totals, broken down by model, account, key or provider",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.since, "since", "", "from this time (RFC 3339) or this long ago (such as 24h or 7d)")
			fs.StringVar(&o.until, "until", "", "up to this time (RFC 3339)")
			fs.StringVar(&o.by, "by", "", "breakdown to show: model, account, key or provider (default: model)")
			fs.StringVar(&o.model, "model", "", "only this model")
			fs.StringVar(&o.account, "account", "", "only this account")
			fs.StringVar(&o.provider, "provider", "", "only this provider (extension)")
			fs.StringVar(&o.clientKey, "client-key", "", "only this client key ID")
		},
		run: func(x *invocation) error {
			q := url.Values{}
			for k, v := range map[string]string{
				"since":    x.opts.since,
				"until":    x.opts.until,
				"by":       x.opts.by,
				"model":    x.opts.model,
				"account":  x.opts.account,
				"provider": x.opts.provider,
				"key":      x.opts.clientKey,
			} {
				if v != "" {
					q.Set(k, v)
				}
			}
			var u management.UsageReply
			return x.call(http.MethodGet, "/usage", q, nil, &u, func() {
				if !u.Enabled {
					x.printf("usage is not recorded (usage-statistics-enabled is off)\n")
				}
				x.printf("%d requests, %d failed, %d input and %d output tokens\n", u.Requests, u.Failure, u.Tokens.Input, u.Tokens.Output)
				header := "MODEL"
				groups := u.Models
				switch u.By {
				case "account":
					header = "ACCOUNT"
					groups = u.Breakdown
				case "key":
					header = "KEY"
					groups = u.Breakdown
				case "provider":
					header = "PROVIDER"
					groups = u.Breakdown
				case "model":
					header = "MODEL"
					groups = u.Breakdown
				}
				if len(groups) == 0 {
					return
				}
				var rows [][]string
				if header == "ACCOUNT" {
					rows = [][]string{{"ACCOUNT", "PROVIDER", "REQUESTS", "FAILED", "INPUT", "OUTPUT"}}
					for _, g := range groups {
						rows = append(rows, []string{g.Name, dash(g.Provider), strconv.FormatInt(g.Requests, 10), strconv.FormatInt(g.Failure, 10),
							strconv.FormatInt(g.Tokens.Input, 10), strconv.FormatInt(g.Tokens.Output, 10)})
					}
				} else {
					rows = [][]string{{header, "REQUESTS", "FAILED", "INPUT", "OUTPUT"}}
					for _, g := range groups {
						rows = append(rows, []string{g.Name, strconv.FormatInt(g.Requests, 10), strconv.FormatInt(g.Failure, 10),
							strconv.FormatInt(g.Tokens.Input, 10), strconv.FormatInt(g.Tokens.Output, 10)})
					}
				}
				x.table(rows)
			})
		}},
	{words: "usage events", routes: []string{"GET /usage/events"}, summary: "recorded requests, oldest first or newest first with --before",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.after, "after", "", "oldest first: only events with seq above this")
			fs.StringVar(&o.before, "before", "", "newest first: only events with seq below this (0 or no value for the latest page)")
			fs.IntVar(&o.limit, "limit", 0, "at most this many events (default: core's, 100)")
			fs.StringVar(&o.since, "since", "", "from this time (RFC 3339) or this long ago (such as 24h or 7d)")
			fs.StringVar(&o.until, "until", "", "up to this time (RFC 3339)")
			fs.StringVar(&o.model, "model", "", "only this model")
			fs.StringVar(&o.account, "account", "", "only this account")
			fs.StringVar(&o.provider, "provider", "", "only this provider (extension)")
			fs.StringVar(&o.clientKey, "client-key", "", "only this client key ID")
		},
		run: func(x *invocation) error {
			q := url.Values{}
			for k, v := range map[string]string{
				"after":    x.opts.after,
				"before":   x.opts.before,
				"since":    x.opts.since,
				"until":    x.opts.until,
				"model":    x.opts.model,
				"account":  x.opts.account,
				"provider": x.opts.provider,
				"key":      x.opts.clientKey,
			} {
				if v != "" {
					q.Set(k, v)
				}
			}
			if x.opts.limit > 0 {
				q.Set("limit", strconv.Itoa(x.opts.limit))
			}
			var r management.UsageEventsReply
			return x.call(http.MethodGet, "/usage/events", q, nil, &r, func() {
				if !r.Enabled {
					x.printf("usage is not recorded (usage-statistics-enabled is off)\n")
				}
				if len(r.Events) == 0 {
					x.printf("no matching events\n")
					return
				}
				rows := [][]string{{"SEQ", "TIME", "MODEL", "ACCOUNT", "KEY", "STATUS", "LATENCY", "TOKENS"}}
				for _, ev := range r.Events {
					status := strconv.Itoa(ev.Status)
					if ev.Status == 0 {
						status = "-"
					}
					if ev.Failed {
						if ev.ErrorCode != "" {
							status = status + " (" + ev.ErrorCode + ")"
						} else {
							status = status + " (failed)"
						}
					}
					tokens := "-"
					if ev.Tokens.Total != nil {
						tokens = strconv.FormatInt(*ev.Tokens.Total, 10)
					}
					rows = append(rows, []string{
						strconv.FormatUint(ev.Seq, 10),
						ev.Timestamp.Local().Format(time.DateTime),
						dash(ev.Model),
						dash(ev.Account),
						dash(ev.KeyID),
						status,
						fmt.Sprintf("%dms", ev.LatencyMS),
						tokens,
					})
				}
				x.table(rows)
			})
		}},
	usageExportCommand,
	{words: "theme list", routes: []string{"GET /themes"}, summary: "the selected theme and stored themes",
		run: func(x *invocation) error {
			var r management.ThemesReply
			return x.call(http.MethodGet, "/themes", nil, nil, &r, func() {
				x.printf("current: %s\n", dash(r.Current))
				if len(r.Themes) == 0 {
					return
				}
				rows := [][]string{{"ID", "NAME", "BYTES"}}
				for _, t := range r.Themes {
					rows = append(rows, []string{t.ID, dash(t.Name), strconv.Itoa(t.Bytes)})
				}
				x.table(rows)
			})
		}},
	{words: "theme show", args: []string{"id"}, routes: []string{"GET /themes/{publisher}/{name}"}, summary: "a stored theme's JSON",
		run: func(x *invocation) error {
			path, err := themeAPIPath(x.args[0])
			if err != nil {
				return err
			}
			body, err := x.client.Call(x.ctx, http.MethodGet, path, nil, nil, nil)
			if err != nil {
				return err
			}
			if _, err := x.stdout.Write(body); err != nil {
				return err
			}
			if !x.json && !bytes.HasSuffix(body, []byte("\n")) {
				x.printf("\n")
			}
			return nil
		}},
	{words: "theme add", args: []string{"file"}, routes: []string{"PUT /themes/{publisher}/{name}", "GET /themes"}, summary: "store a theme JSON file",
		flags: func(fs *flag.FlagSet, o *opts) {
			fs.StringVar(&o.id, "id", "", "theme ID (publisher/name)")
		},
		run: func(x *invocation) error {
			raw, err := os.ReadFile(x.args[0])
			if err != nil {
				return err
			}
			id := resolveThemeID(x.opts.id, x.args[0], raw)
			if err := checkNewThemeID(id); err != nil {
				return err
			}
			// With --json the reply is printed unchanged, so it is not decoded into the
			// summary first: a field a newer core changed the type of would abort it.
			var summary management.ThemeSummary
			var out any = &summary
			if x.json {
				out = nil
			}
			body, err := x.client.CallBytes(x.ctx, http.MethodPut, management.ThemePath(id), nil, raw, out)
			if err != nil {
				return err
			}
			if x.json {
				_, err := x.stdout.Write(body)
				return err
			}
			x.printf("%s\n", summary.ID)
			// Standard output stays the ID alone. When core does not say what is selected, say nothing.
			var themes struct {
				Current string `json:"current"`
			}
			if _, err := x.client.Call(x.ctx, http.MethodGet, "/themes", nil, nil, &themes); err == nil && themes.Current != summary.ID {
				x.eprintf("Stored but not turned on. To turn it on, run: pluxy theme set %s\n", summary.ID)
			}
			return nil
		}},
	{words: "theme remove", args: []string{"id"}, routes: []string{"DELETE /themes/{publisher}/{name}"}, summary: "remove a stored theme",
		run: func(x *invocation) error {
			path, err := themeAPIPath(x.args[0])
			if err != nil {
				return err
			}
			return x.call(http.MethodDelete, path, nil, nil, nil, func() {
				x.printf("theme %s removed\n", x.args[0])
			})
		}},
	{words: "theme set", args: []string{"id"}, routes: []string{"PUT /theme"}, summary: "select a theme ID",
		run: func(x *invocation) error {
			var r management.ThemeSelection
			return x.call(http.MethodPut, "/theme", nil, management.ThemeSelection{ID: x.args[0]}, &r, func() {
				x.printf("theme %s selected\n", r.ID)
			})
		}},
	{words: "theme clear", routes: []string{"PUT /theme"}, summary: "clear the selected theme",
		run: func(x *invocation) error {
			var r management.ThemeSelection
			return x.call(http.MethodPut, "/theme", nil, management.ThemeSelection{ID: ""}, &r, func() {
				x.printf("theme cleared\n")
			})
		}},
}

func resolveThemeID(flagID, filePath string, raw []byte) string {
	if flagID != "" {
		return flagID
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		var id, name string
		if err := json.Unmarshal(fields["id"], &id); err != nil {
			id = ""
		}
		if err := json.Unmarshal(fields["name"], &name); err != nil {
			name = ""
		}
		if _, pluxyFile := fields["theme_version"]; !pluxyFile {
			if validT3ThemeID(id) {
				return "t3/" + id
			}
			if strings.TrimSpace(name) != "" {
				return "t3/" + themeIDFromName(name)
			}
		} else if validThemeID(id) {
			return id
		}
	}
	return themeFileID(filePath)
}

func themeFileID(filePath string) string {
	base := filepath.Base(filePath)
	if len(base) >= 5 && strings.EqualFold(base[len(base)-5:], ".json") {
		base = base[:len(base)-5]
	}
	var b strings.Builder
	for _, r := range strings.ToLower(base) {
		if b.Len() >= 64 {
			break
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return "t3/" + b.String()
}

func validT3ThemeID(id string) bool {
	if len(id) < 1 || len(id) > 48 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || (i > 0 && c == '-') {
			continue
		}
		return false
	}
	return true
}

// themeIDFromName matches themeIdFromName in next/ui/kit/theme/t3.ts.
func themeIDFromName(name string) string {
	label := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	hyphen := false
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
		} else if b.Len() > 0 && !hyphen {
			b.WriteByte('-')
			hyphen = true
		}
	}
	normalized := strings.Trim(b.String(), "-")
	if len(normalized) > 48 {
		normalized = normalized[:48]
	}
	if normalized != "" {
		return normalized
	}
	if label == "" {
		return "custom-theme"
	}

	hash := uint32(2166136261)
	for _, unit := range utf16.Encode([]rune(label)) {
		hash ^= uint32(unit)
		hash *= 16777619
	}
	return "theme-" + strconv.FormatUint(uint64(hash), 36)
}

func validThemeID(id string) bool {
	pub, name, ok := strings.Cut(id, "/")
	if !ok || pub == "current" {
		return false
	}
	return validThemePart(pub) && validThemePart(name)
}

// checkNewThemeID refuses an ID theme add cannot store, before any request, and says what a valid
// one looks like.
func checkNewThemeID(id string) error {
	if validThemeID(id) {
		return nil
	}
	if pub, name, ok := strings.Cut(id, "/"); ok && pub == "current" && validThemePart(name) {
		return usageError{fmt.Sprintf(`theme ID %q is not valid: "current" cannot be the publisher, so use another, such as my/%s`, id, name)}
	}
	return usageError{fmt.Sprintf("theme ID %q is not valid: write it as publisher/name, such as my/paper, with each part 1 to 64 characters from a-z, 0-9 and -", id)}
}

// themeAPIPath is the API path of a theme ID from the command line. The commands that take one
// ID refuse a one-part ID here: it would make a path with an empty part, which core answers as a
// theme that is not there.
func themeAPIPath(id string) (string, error) {
	if !validThemeID(id) {
		return "", usageError{fmt.Sprintf("theme ID %q is not valid: write it as publisher/name", id)}
	}
	return management.ThemePath(id), nil
}

func validThemePart(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// windowText writes a usage window's length in whole days or hours where it has them, such as
// 7d or 5h.
func windowText(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return d.String()
}

// chainText writes a chain's steps as chain set takes them.
func chainText(steps []management.ChainStep) string {
	parts := make([]string, len(steps))
	for i, s := range steps {
		parts[i] = s.String()
	}
	return strings.Join(parts, " -> ")
}

// parseStep reads a step as chain set takes it: model[@provider][:account,account]. What follows
// the last @ is the provider only when it is an extension's ID (publisher/name), or empty;
// otherwise the @ is part of the model's name (claude-opus-4-7@20260416). Without a provider, the
// model ends at the last colon, so a model whose name holds a colon is written with an @ after it
// (llama3:8b@ for any provider, llama3:8b@pluxy/ollama for one).
func parseStep(s string) (management.ChainStep, error) {
	step := management.ChainStep{Model: s}
	accounts, hasAccounts := "", false
	i := strings.LastIndexByte(s, '@')
	provider := ""
	if i >= 0 {
		provider, _, _ = strings.Cut(s[i+1:], ":")
	}
	if i >= 0 && (provider == "" || host.ValidID(provider)) {
		step.Model = s[:i]
		step.Provider, accounts, hasAccounts = strings.Cut(s[i+1:], ":")
	} else if i := strings.LastIndexByte(s, ':'); i >= 0 {
		step.Model, accounts, hasAccounts = s[:i], s[i+1:], true
	}
	if hasAccounts {
		for _, a := range strings.Split(accounts, ",") {
			if a == "" {
				return step, usageError{fmt.Sprintf("step %q: an account ID is empty", s)}
			}
			step.Accounts = append(step.Accounts, a)
		}
	}
	if step.Model == "" {
		return step, usageError{fmt.Sprintf("step %q names no model", s)}
	}
	return step, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func normalizeOptionalFlagValue(args []string, name, def string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i, a := range out {
		if a == "--"+name || a == "-"+name {
			if i+1 == len(out) || strings.HasPrefix(out[i+1], "-") {
				out[i] = a + "=" + def
			}
		}
	}
	return out
}

func packageVerb(verb, summary string) command {
	return command{words: "ext " + verb, args: []string{"id"}, routes: []string{"POST /packages/{publisher}/{name}/" + verb}, summary: summary,
		run: func(x *invocation) error {
			var r management.PackageReply
			return x.call(http.MethodPost, management.PackagePath(x.args[0])+"/"+verb, nil, nil, &r, func() {
				x.printf("%s %sd: %s\n", r.Package.ID, verb, r.Package.State)
			})
		}}
}

func accountSwitch(verb string, disabled bool, summary string) command {
	return command{words: "account " + verb, args: []string{"id"}, routes: []string{"PATCH /accounts/{id}"}, summary: summary,
		run: func(x *invocation) error {
			var r management.AccountReply
			return x.call(http.MethodPatch, accountPath(x.args[0]), nil, management.AccountPatch{Disabled: &disabled}, &r, func() {
				x.printf("account %s %sd\n", r.Account.ID, verb)
			})
		}}
}

func printStatus(x *invocation, s management.Status) {
	x.printf("%s\n", buildinfo.Format(s.Version, s.Commit, s.Built))
	x.printf("config revision %d active", s.Active.Revision)
	if s.Desired.Revision != s.Active.Revision {
		x.printf("; revision %d refused: %s", s.Desired.Revision, s.Desired.Error)
	}
	x.printf("\n")
	if s.FirstRun {
		x.printf("first run: setup has not been completed (run pluxy setup)\n")
	}
	if s.SafeMode {
		x.printf("safe mode: no extension runs\n")
	}
	if len(s.Extensions) == 0 {
		return
	}
	rows := [][]string{{"EXTENSION", "VERSION", "STATE", "GENERATION", "CURRENT", "ACTIVE REQUESTS"}}
	for _, e := range s.Extensions {
		state := e.State
		if e.LastError != "" {
			state += ": " + e.LastError
		}
		rows = append(rows, []string{e.ID, e.Version, state, strconv.FormatUint(e.Generation, 10), yesNo(e.Current), strconv.Itoa(e.ActiveRequests)})
	}
	x.table(rows)
}

// printFlat prints v's JSON as one "name value" line per leaf, names joined with dots, in
// sorted order.
func printFlat(x *invocation, v any) {
	b, _ := json.Marshal(v)
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var tree any
	if d.Decode(&tree) != nil {
		return
	}
	var rows [][]string
	var walk func(prefix string, t any)
	walk = func(prefix string, t any) {
		switch t := t.(type) {
		case map[string]any:
			for k, v := range t {
				walk(strings.TrimPrefix(prefix+"."+k, "."), v)
			}
		case []any:
			if len(t) == 0 {
				rows = append(rows, []string{prefix, "[]"})
			}
			for i, v := range t {
				walk(fmt.Sprintf("%s[%d]", prefix, i), v)
			}
		case nil:
			rows = append(rows, []string{prefix, "null"})
		default:
			rows = append(rows, []string{prefix, fmt.Sprint(t)})
		}
	}
	walk("", tree)
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	x.table(rows)
}

func formatCapabilities(c *protocol.ModelCapabilities) string {
	if c == nil {
		return "-"
	}
	var parts []string
	if c.Tools {
		parts = append(parts, "tools")
	}
	if c.StructuredOutput {
		parts = append(parts, "structured_output")
	}
	if c.Vision {
		parts = append(parts, "vision")
	}
	if c.PDF {
		parts = append(parts, "pdf")
	}
	if c.Reasoning {
		parts = append(parts, "reasoning")
	}
	if c.DeprecatedAt > 0 {
		parts = append(parts, "deprecated")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// argsUsage is how help and usage errors write cmd's arguments.
func argsUsage(cmd *command) string {
	out := ""
	for i, a := range cmd.args {
		if cmd.optional && i == len(cmd.args)-1 {
			out += " [" + a + "]"
			continue
		}
		out += " <" + a + ">"
	}
	if cmd.more {
		out += "..."
	}
	return out
}

// find returns the command args start with, and the arguments after its words.
func find(args []string) (*command, []string) {
	var best *command
	n := 0
	for i := range commands {
		w := strings.Fields(commands[i].words)
		if len(w) > len(args) || len(w) <= n || !slices.Equal(w, args[:len(w)]) {
			continue
		}
		best, n = &commands[i], len(w)
	}
	return best, args[n:]
}

// groupPrefix is args0 and a space when commands name a group by it (account, ext, ...), else "".
func groupPrefix(word string) string {
	prefix := word + " "
	if slices.ContainsFunc(commands, func(c command) bool { return strings.HasPrefix(c.words, prefix) }) {
		return prefix
	}
	return ""
}

// groupHelp writes a group's commands and their one-line summaries, for a command line that
// names the group without a command it has.
func groupHelp(w io.Writer, prefix string) {
	fmt.Fprintf(w, "usage: pluxy %s<command> [flags]\n", prefix)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i := range commands {
		c := &commands[i]
		if !strings.HasPrefix(c.words+" ", prefix) {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\n", c.words+argsUsage(c), c.summary)
	}
	tw.Flush()
}

// isHelpFlag reports whether arg is one of the spellings of help a command's own flags take.
func isHelpFlag(arg string) bool {
	return slices.Contains([]string{"-h", "--h", "-help", "--help"}, arg)
}

// help writes the list of commands whose words start with prefix.
func help(w io.Writer, prefix string) {
	fmt.Fprintln(w, "usage: pluxy serve [-config file] [flags]   run core (pluxy with no subcommand does too)")
	fmt.Fprintln(w, "       pluxy init [-config file] [-port n]  write a first config (pluxy serve does when there is none)")
	fmt.Fprintln(w, "       pluxy <command> [--json] [--config file | --url address --key key]")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i := range commands {
		c := &commands[i]
		if !strings.HasPrefix(c.words+" ", prefix) {
			continue
		}
		line := c.words + argsUsage(c)
		fmt.Fprintf(tw, "  %s\t%s\n", line, c.summary)
	}
	tw.Flush()
}

// runCLI runs one client subcommand and returns its exit code.
func runCLI(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	return runCLIWithInput(args, os.Stdin, stdout, stderr, getenv, isInteractive(os.Stdin))
}

func runCLIWithInput(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string, tty bool) int {
	if args[0] == "help" {
		help(stdout, strings.Join(args[1:], " "))
		return exitOK
	}
	cmd, rest := find(args)
	if cmd == nil {
		// A group's word names the commands it holds: alone (pluxy account), with one it does
		// not have (pluxy account frobnicate), or with words it does not take (pluxy settings
		// set). A command's word alone with more after it (pluxy status now) is that command's
		// extra argument, which its own check below refuses. Asked for help, it lists them as
		// the answer rather than as a usage error.
		if prefix := groupPrefix(args[0]); prefix != "" {
			if len(args) > 1 && isHelpFlag(args[1]) {
				groupHelp(stdout, prefix)
				return exitOK
			}
			if len(args) > 1 {
				fmt.Fprintf(stderr, "pluxy: unknown %s command %q\n", args[0], args[1])
			} else {
				fmt.Fprintf(stderr, "pluxy: %s needs a command\n", args[0])
			}
			groupHelp(stderr, prefix)
		} else if slices.ContainsFunc(commands, func(c command) bool { return c.words == args[0] }) {
			fmt.Fprintf(stderr, "pluxy: %s takes no subcommand %q\n", args[0], args[1])
			help(stderr, "")
		} else {
			fmt.Fprintf(stderr, "pluxy: unknown command %q\n", args[0])
			help(stderr, "")
		}
		return exitUsage
	}
	fs := flag.NewFlagSet("pluxy "+cmd.words, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o opts
	jsonOut := fs.Bool("json", false, "print the API's reply body unchanged")
	configPath := fs.String("config", "config.json", "the config file to read core's address and management key from")
	addr := fs.String("url", "", "core's address, such as http://127.0.0.1:8317 (default: $PLUXY_URL, else from the config)")
	key := fs.String("key", "", "the management key (default: $PLUXY_MANAGEMENT_KEY, else from the config)")
	if cmd.flags != nil {
		cmd.flags(fs, &o)
	}
	if cmd.words == "usage events" {
		rest = normalizeOptionalFlagValue(rest, "before", "0")
	}
	// Flags may come before, between and after the arguments.
	var pos []string
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return exitOK
			}
			return exitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(pos) != len(cmd.args) && !(cmd.more && len(pos) > len(cmd.args)) && !(cmd.optional && len(pos) == len(cmd.args)-1) {
		fmt.Fprintf(stderr, "pluxy: usage: pluxy %s%s [flags]\n", cmd.words, argsUsage(cmd))
		return exitUsage
	}
	// A --input pair the flag could not take is refused here, where nothing quotes the value it
	// was given.
	if o.input.bad != nil {
		printError(stderr, *jsonOut, usageError{"--input: " + o.input.bad.Error()})
		return exitUsage
	}
	t, err := findCore(*configPath, cmpOr(*addr, getenv("PLUXY_URL")), cmpOr(*key, getenv("PLUXY_MANAGEMENT_KEY")))
	if err != nil {
		printError(stderr, *jsonOut, err)
		return exitFailed
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	x := &invocation{ctx: ctx, client: &management.Client{URL: t.url, Key: t.key, HTTP: t.http}, args: pos, opts: o, json: *jsonOut, stdin: stdin, stdout: stdout, stderr: stderr, tty: tty}
	err = cmd.run(x)
	if err == nil {
		return exitOK
	}
	printError(stderr, *jsonOut, err)
	return exitCode(err, cmd.changesNothing())
}

// printError writes a command's error to stderr: with --json, as the API's error object (the
// reply's own body when core sent one), otherwise as a line of text.
func printError(stderr io.Writer, asJSON bool, err error) {
	if !asJSON {
		fmt.Fprintf(stderr, "pluxy: %v\n", err)
		return
	}
	var ae *management.APIError
	if errors.As(err, &ae) && json.Valid(ae.Body) {
		stderr.Write(ae.Body)
		if !bytes.HasSuffix(ae.Body, []byte("\n")) {
			fmt.Fprintln(stderr)
		}
		return
	}
	code, msg := "failed", err.Error()
	var ue usageError
	var we waitingError
	switch {
	case errors.As(err, &ae):
		code, msg = ae.Code, ae.Message
	case errors.As(err, &ue):
		code = "invalid_request"
	case errors.As(err, &we):
		code = "waiting"
	case management.IsUnreachable(err):
		code = "unreachable"
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	fmt.Fprintf(stderr, "%s\n", b)
}

// changesNothing reports whether the command only reads: it is marked readOnly, or every route it
// calls is a GET.
func (c command) changesNothing() bool {
	if c.readOnly {
		return true
	}
	for _, r := range c.routes {
		if !strings.HasPrefix(r, "GET ") {
			return false
		}
	}
	return len(c.routes) > 0
}

// exitCode is the exit code for a command's error. readOnly says the command changes nothing,
// so its 409 or 422 failed a call rather than refusing a change.
func exitCode(err error, readOnly bool) int {
	var ue usageError
	var we waitingError
	var ae *management.APIError
	switch {
	case errors.As(err, &ue):
		return exitUsage
	case errors.As(err, &we):
		return exitWaiting
	case management.IsUnreachable(err):
		return exitUnreachable
	case errors.As(err, &ae):
		switch ae.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return exitRefused
		case http.StatusNotFound:
			return exitNotFound
		case http.StatusConflict, http.StatusUnprocessableEntity:
			if readOnly {
				return exitFailed
			}
			return exitChange
		}
	}
	return exitFailed
}
