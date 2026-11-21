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
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func setupCommands() []command {
	return []command{
		{
			words:   "setup",
			routes:  []string{"GET /setup"},
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
			routes:  []string{"POST /setup/apply"},
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
		return applySuggested(x, suggestedUninstalled(s.Picks))
	}
	var s management.Setup
	if err := x.call(http.MethodGet, "/setup", nil, nil, &s, func() {
		printSetup(x, s)
	}); err != nil {
		return err
	}
	todo := suggestedUninstalled(s.Picks)
	if x.json || !x.tty || len(todo) == 0 {
		return nil
	}
	if !confirm(x.stdin, x.stderr, fmt.Sprintf("Apply suggested picks (%s)? [y/N]: ", strings.Join(todo, ", "))) {
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
		return nil
	}
	return applySuggested(x, todo)
}

// applySuggested applies the suggested picks not installed yet. With none, there is no reply to
// print: with --json that is a usage error, otherwise a line says so.
func applySuggested(x *invocation, todo []string) error {
	if len(todo) == 0 {
		if x.json {
			return usageError{"no suggested picks to apply: name one with pluxy setup apply --pick <id>"}
		}
		x.printf("no suggested picks to apply\n")
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
	return nil
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
		rows := [][]string{{"KIND", "CONFIG", "AUTH DIR", "ACCOUNTS"}}
		for _, im := range s.Found.Imports {
			rows = append(rows, []string{im.Kind, im.Config, im.AuthDir, fmt.Sprintf("%d", im.Accounts)})
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
	return x.call(http.MethodPost, "/setup/import", nil, req, &r, func() {
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
}
