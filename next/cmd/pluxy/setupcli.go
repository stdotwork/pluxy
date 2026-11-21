package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stdotwork/pluxy/next/internal/core"
	"github.com/stdotwork/pluxy/next/management"
)

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

type stringSliceFlag []string

func (s *stringSliceFlag) String() string { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("pick ID cannot be empty")
	}
	*s = append(*s, v)
	return nil
}

// isInteractive reports whether r is a terminal (e.g. os.Stdin on a TTY).
var isInteractive = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return false
	}
	return isTerminalFile(f)
}

func setupCommands() []command {
	return []command{
		{
			words:   "setup",
			routes:  []string{"GET /setup", "GET /accounts", "GET /login-methods"},
			summary: "show what core found on this machine and suggested packages",
			flags: func(fs *flag.FlagSet, o *opts) {
				fs.BoolVar(&o.yes, "yes", false, "apply suggested picks without asking")
			},
			run: runSetup,
		},
		{
			words:   "setup scan",
			routes:  []string{"POST /setup/scan"},
			summary: "look for client tools, sign-ins and existing setups again",
			run: func(x *invocation) error {
				var s management.Setup
				return x.call(http.MethodPost, "/setup/scan", nil, nil, &s, func() {
					x.printf("scan started (scanning: %v); run pluxy setup to see the result\n", s.Scanning)
				})
			},
		},
		{
			words:   "setup apply",
			routes:  []string{"POST /setup/apply", "GET /setup", "GET /accounts", "GET /login-methods"},
			summary: "install and enable setup picks",
			flags: func(fs *flag.FlagSet, o *opts) {
				fs.Var(&o.picks, "pick", "package ID to install and enable (may be repeated)")
				fs.BoolVar(&o.yes, "yes", false, "apply suggested picks without asking when --pick is not given")
			},
			run: runSetupApply,
		},
		{
			words:   "setup done",
			routes:  []string{"POST /setup/done"},
			summary: "end first-run setup",
			run: func(x *invocation) error {
				var r management.SetupDoneReply
				return x.call(http.MethodPost, "/setup/done", nil, nil, &r, func() {
					x.printf("first-run setup complete\n")
				})
			},
		},
		{
			words:   "import cpa",
			args:    []string{"config.yaml"},
			routes:  []string{"POST /setup/import"},
			summary: "bring a CLIProxyAPI setup (its config.yaml and auth folder) across and print what came, what changed and what did not",
			flags: func(fs *flag.FlagSet, o *opts) {
				fs.StringVar(&o.authDir, "auth-dir", "", "the auth folder (default: the config's oauth.auth-dir)")
				fs.BoolVar(&o.dryRun, "dry-run", false, "report what the import would do, and write nothing")
			},
			run: runImportCPA,
		},
		{
			words:   "client show",
			args:    []string{"tool"},
			routes:  []string{"GET /setup/clients/{id}/paste"},
			summary: "print what to paste into a client tool's config to point it at core",
			flags: func(fs *flag.FlagSet, o *opts) {
				fs.StringVar(&o.keyID, "key-id", "", "client key ID from pluxy key list to reference")
			},
			run: runClientShow,
		},
	}
}

func runSetup(x *invocation) error {
	if x.opts.yes {
		s, err := x.client.Setup(x.ctx)
		if err != nil {
			return err
		}
		if !x.json {
			printSetup(x, s)
		}
		return applySuggested(x, s)
	}
	var s management.Setup
	if err := x.call(http.MethodGet, "/setup", nil, nil, &s, func() {
		printSetup(x, s)
	}); err != nil {
		return err
	}
	if x.json {
		return nil
	}
	todo := suggestedUninstalled(s.Picks)
	if !x.tty || len(todo) == 0 ||
		!confirm(x.stdin, x.stderr, fmt.Sprintf("Apply suggested picks (%s)? [y/N]: ", strings.Join(todo, ", "))) {
		x.nextStep(s)
		return nil
	}
	return applySetupPicks(x, todo)
}

func runSetupApply(x *invocation) error {
	if picks := []string(x.opts.picks); len(picks) > 0 {
		return applySetupPicks(x, picks)
	}
	if !x.opts.yes && !x.tty {
		return usageError{"pass --yes or --pick <id> when standard input is not a terminal"}
	}
	s, err := x.client.Setup(x.ctx)
	if err != nil {
		return err
	}
	todo := suggestedUninstalled(s.Picks)
	if len(todo) > 0 && !x.opts.yes &&
		!confirm(x.stdin, x.stderr, fmt.Sprintf("Apply suggested picks (%s)? [y/N]: ", strings.Join(todo, ", "))) {
		if !x.json {
			x.nextStep(s)
		}
		return nil
	}
	return applySuggested(x, s)
}

// applySuggested applies s's suggested picks not installed yet. With none, there is no reply to
// print: with --json that is a usage error, otherwise a line says so.
func applySuggested(x *invocation, s management.Setup) error {
	todo := suggestedUninstalled(s.Picks)
	if len(todo) == 0 {
		if x.json {
			return usageError{"no suggested picks to apply: name one with pluxy setup apply --pick <id>"}
		}
		x.printf("no suggested picks to apply\n")
		x.nextStep(s)
		return nil
	}
	return applySetupPicks(x, todo)
}

func applySetupPicks(x *invocation, picks []string) error {
	var r management.SetupApplyReply
	err := x.call(http.MethodPost, "/setup/apply", nil, management.SetupApplyRequest{Picks: picks}, &r, func() {
		rows := [][]string{{"PICK", "STATUS", "DETAIL"}}
		for _, res := range r.Results {
			detail := "-"
			if res.Error != "" {
				detail = res.Error
			} else if res.Package != nil {
				detail = res.Package.Version + " (" + res.Package.State + ")"
			}
			rows = append(rows, []string{res.ID, res.Status, detail})
		}
		x.table(rows)
	})
	if err != nil {
		return err
	}
	failed := 0
	for _, res := range r.Results {
		if res.Status == management.SetupApplyError {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d picks failed", failed, len(r.Results))
	}
	if !x.json {
		// The step that follows is the one for the state the picks left core in.
		if s, err := x.client.Setup(x.ctx); err == nil {
			x.nextStep(s)
		}
	}
	return nil
}

// nextStep writes one line naming what to do next while first-run setup has not ended, for the
// state core is in: s, and for the later steps its accounts and the providers that can add one.
// It is a note for a person, so it goes to standard error, as a command's other notes do, and
// standard output stays the tables. It writes nothing once setup has ended, or when core does
// not answer what the line needs.
func (x *invocation) nextStep(s management.Setup) {
	if !s.FirstRun {
		return
	}
	line := func() string {
		if todo := suggestedUninstalled(s.Picks); len(todo) > 0 {
			return fmt.Sprintf("pluxy setup apply --yes installs what core suggests (%s).", strings.Join(todo, ", "))
		}
		if s.Scanning {
			// The suggestions follow from what the scan finds.
			return "core is still looking at this machine: run pluxy setup again in a moment."
		}
		accounts, err := x.client.Accounts(x.ctx)
		if err != nil {
			return ""
		}
		if len(accounts) > 0 {
			for _, c := range s.Found.Clients {
				if c.Installed && c.State != "configured" && slices.Contains(core.ClientSnippetTools, c.ID) {
					return fmt.Sprintf("pluxy client show %s prints what to paste into %s's config; then pluxy setup done ends first-run setup.", c.ID, cmpOr(c.Name, c.ID))
				}
			}
			return "pluxy setup done ends first-run setup."
		}
		methods, err := x.loginMethods()
		if err != nil {
			return ""
		}
		// An account is added through a running provider, which is what offers a way to sign in.
		var providers []string
		for _, e := range methods.Extensions {
			if len(e.Methods) > 0 {
				providers = append(providers, e.Extension)
			}
		}
		switch len(providers) {
		case 0:
			for _, p := range s.Picks {
				if p.Kind == "provider" && !p.Installed {
					return "install a provider with pluxy setup apply --pick <id>, an ID from the picks pluxy setup lists."
				}
			}
			return "no provider is running, so no account can be added yet: pluxy ext list shows what is installed, and pluxy ext available what can be installed."
		case 1:
			return fmt.Sprintf("add an account with pluxy account add %s (pluxy account methods lists the ways to sign in).", providers[0])
		}
		return "add an account with pluxy account add <provider> (pluxy account methods lists the providers and their ways to sign in)."
	}()
	if line != "" {
		x.eprintf("Next: %s\n", line)
	}
}

func suggestedUninstalled(picks []management.SetupPick) []string {
	var todo []string
	for _, p := range picks {
		if p.Suggested && !p.Installed {
			todo = append(todo, p.ID)
		}
	}
	return todo
}

func confirm(in io.Reader, out io.Writer, prompt string) bool {
	if in == nil {
		return false
	}
	fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && len(line) == 0 {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

func printSetup(x *invocation, s management.Setup) {
	status := "not scanned"
	if s.Scanning {
		status = "scanning"
	} else if s.ScannedAt != nil {
		status = "scanned " + s.ScannedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	x.printf("first_run: %v  scan: %s  core: %s (network: %v)\n",
		s.FirstRun, status, dash(s.Found.Core.Listen), s.Found.Core.ReachableFromNetwork)
	if len(s.Found.Clients) > 0 {
		x.printf("\nClients:\n")
		rows := [][]string{{"CLIENT", "INSTALLED", "VERSION", "STATE", "CONFIG"}}
		for _, c := range s.Found.Clients {
			rows = append(rows, []string{c.ID, yesNo(c.Installed), dash(c.Version), c.State, c.Config})
		}
		x.table(rows)
	}
	if len(s.Found.SignIns) > 0 {
		x.printf("\nSign-ins:\n")
		rows := [][]string{{"PROVIDER", "FROM", "LABEL"}}
		for _, si := range s.Found.SignIns {
			rows = append(rows, []string{si.Provider, si.From, si.Label})
		}
		x.table(rows)
	}
	if len(s.Found.Imports) > 0 {
		x.printf("\nImports:\n")
		rows := [][]string{{"KIND", "CONFIG", "AUTH DIR", "ACCOUNTS", "BROUGHT ACROSS"}}
		for _, im := range s.Found.Imports {
			// When core last brought this setup across (pluxy import cpa, or the Setup page).
			brought := "no"
			if im.LastImport != nil {
				brought = im.LastImport.At.UTC().Format("2006-01-02T15:04:05Z")
			}
			rows = append(rows, []string{im.Kind, im.Config, im.AuthDir, fmt.Sprintf("%d", im.Accounts), brought})
		}
		x.table(rows)
	}
	if len(s.Picks) > 0 {
		x.printf("\nPicks:\n")
		rows := [][]string{{"ID", "KIND", "TITLE", "INSTALLED", "SUGGESTED", "BECAUSE"}}
		for _, p := range s.Picks {
			rows = append(rows, []string{p.ID, p.Kind, p.Title, yesNo(p.Installed), yesNo(p.Suggested), dash(p.Because)})
		}
		x.table(rows)
	}
}

func runClientShow(x *invocation) error {
	if _, ok := core.ClientSnippetFor(x.args[0], "", ""); !ok {
		return usageError{fmt.Sprintf("unknown client tool %q (supported: %s)", x.args[0], strings.Join(core.ClientSnippetTools, ", "))}
	}
	var q url.Values
	if x.opts.keyID != "" {
		q = url.Values{"key_id": {x.opts.keyID}}
	}
	var p management.ClientPaste
	return x.call(http.MethodGet, "/setup/clients/"+url.PathEscape(x.args[0])+"/paste", q, nil, &p, func() {
		x.printf("Client: %s\nAddress: %s\n", p.Name, p.BaseURL)
		for _, f := range p.Files {
			x.printf("\n%s:\n%s\n", f.Path, f.Content)
		}
		if p.Note != "" {
			x.printf("\n%s\n", p.Note)
		}
		switch {
		case p.Key == "<client-key>":
			x.eprintf("Replace <client-key> with a client key: core's config file holds them, and \"pluxy key add\" makes another.\n")
		case strings.HasPrefix(p.Key, "<client-key:"):
			x.eprintf("Replace %s with that key's full value from core's config file: pluxy never prints a stored key.\n", p.Key)
		}
		for _, f := range p.Files {
			if strings.Contains(f.Content, "<model-id>") {
				x.eprintf("Replace <model-id> with the name of a model your accounts serve: \"pluxy model list\" lists them.\n")
				break
			}
		}
	})
}

// runImportCPA is pluxy import cpa. Core reads the files on its own machine, so a relative path
// is made absolute from the current folder first.
func runImportCPA(x *invocation) error {
	abs := func(p string) (string, error) {
		if p == "" || strings.HasPrefix(p, "~") {
			return p, nil
		}
		return filepath.Abs(p)
	}
	cfg, err := abs(x.args[0])
	if err != nil {
		return err
	}
	dir, err := abs(x.opts.authDir)
	if err != nil {
		return err
	}
	var r management.ImportReport
	req := management.SetupImportRequest{Config: cfg, AuthDir: dir, DryRun: x.opts.dryRun}
	err = x.call(http.MethodPost, "/setup/import", nil, req, &r, func() {
		if r.DryRun {
			x.printf("Dry run: nothing was written.\n")
		}
		x.printf("\nBrought (%d):\n", len(r.Brought))
		rows := [][]string{{"WHAT", "NAME", "AS"}}
		for _, b := range r.Brought {
			rows = append(rows, []string{b.What, dash(b.Name), dash(b.As)})
		}
		x.table(rows)
		x.printf("\nChanged (%d):\n", len(r.Changed))
		rows = [][]string{{"WHAT", "NAME", "AS", "FROM", "TO", "WHY", "KEEP WITH"}}
		for _, c := range r.Changed {
			rows = append(rows, []string{c.What, dash(c.Name), dash(c.As), dash(c.From), dash(c.To), dash(c.Why), dash(c.KeepWith)})
		}
		x.table(rows)
		x.printf("\nNot brought (%d):\n", len(r.NotBrought))
		rows = [][]string{{"WHAT", "NAME", "WHY"}}
		for _, n := range r.NotBrought {
			rows = append(rows, []string{n.What, dash(n.Name), dash(n.Why)})
		}
		x.table(rows)
		if len(r.Needs) > 0 {
			x.printf("\nNeeds (install with pluxy ext install <pick>):\n")
			rows = [][]string{{"PICK", "WHY"}}
			for _, n := range r.Needs {
				rows = append(rows, []string{n.Pick, n.Why})
			}
			x.table(rows)
		}
	})
	// A 400 is core saying it could not read what the command named (no such file, not a file,
	// not an absolute path): its sentence is the whole answer, so no error code follows it.
	var ae *management.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusBadRequest && ae.Code != "" {
		return unreadableError{api: ae}
	}
	return err
}

// unreadableError is an API error printed as core's message alone. It is still the API error
// for the exit code and for --json, which prints the reply's body.
type unreadableError struct {
	api *management.APIError
}

func (e unreadableError) Error() string { return e.api.Message }
func (e unreadableError) Unwrap() error { return e.api }
