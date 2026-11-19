package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

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
			words:   "client show",
			args:    []string{"tool"},
			routes:  []string{"GET /setup"},
			summary: "print what to paste into a client tool's config to point it at core",
			flags: func(fs *flag.FlagSet, o *opts) {
				fs.StringVar(&o.keyID, "key-id", "", "client key ID from pluxy key list to reference")
			},
			run: runClientShow,
		},
	}
}

func runSetup(x *invocation) error {
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
	if len(todo) == 0 {
		return nil
	}
	if !x.opts.yes {
		if !x.tty {
			return nil
		}
		if !confirm(x.stdin, x.stderr, fmt.Sprintf("Apply suggested picks (%s)? [y/N]: ", strings.Join(todo, ", "))) {
			return nil
		}
	}
	return applySetupPicks(x, todo)
}

func runSetupApply(x *invocation) error {
	picks := []string(x.opts.picks)
	if len(picks) == 0 {
		s, err := x.client.Setup(x.ctx)
		if err != nil {
			return err
		}
		picks = suggestedUninstalled(s.Picks)
		if len(picks) == 0 {
			if x.json {
				b, _ := json.Marshal(management.SetupApplyReply{Results: []management.SetupApplyResult{}})
				x.printf("%s\n", b)
				return nil
			}
			x.printf("no suggested picks to apply\n")
			return nil
		}
		if !x.opts.yes {
			if !x.tty {
				return errors.New("pass --yes or --pick <id> when standard input is not a terminal")
			}
			if !confirm(x.stdin, x.stderr, fmt.Sprintf("Apply suggested picks (%s)? [y/N]: ", strings.Join(picks, ", "))) {
				return nil
			}
		}
	}
	return applySetupPicks(x, picks)
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
	status := "done"
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

type clientShowSpec struct {
	id     string
	name   string
	config string
	render func(baseURL, keyPlaceholder string) string
}

var clientShowTable = []clientShowSpec{
	{
		id:     "claude-code",
		name:   "Claude Code",
		config: "~/.claude/settings.json",
		render: func(baseURL, key string) string {
			b, _ := json.MarshalIndent(map[string]any{
				"env": map[string]string{
					"ANTHROPIC_BASE_URL":                         baseURL,
					"ANTHROPIC_AUTH_TOKEN":                       key,
					"CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY": "1",
					"CLAUDE_CODE_AUTO_MODE_SERVER":               "0",
				},
			}, "", "  ")
			return string(b)
		},
	},
	{
		id:     "codex",
		name:   "Codex CLI",
		config: "~/.codex/config.toml",
		render: func(baseURL, key string) string {
			v1 := strings.TrimRight(baseURL, "/") + "/v1"
			return fmt.Sprintf(`model_provider = "pluxy"

[model_providers.pluxy]
name = "pluxy"
base_url = %q
wire_api = "responses"
experimental_bearer_token = %q`, v1, key)
		},
	},
	{
		id:     "opencode",
		name:   "OpenCode",
		config: "~/.config/opencode/opencode.jsonc",
		render: func(baseURL, key string) string {
			v1 := strings.TrimRight(baseURL, "/") + "/v1"
			b, _ := json.MarshalIndent(map[string]any{
				"$schema": "https://opencode.ai/config.json",
				"provider": map[string]any{
					"pluxy": map[string]any{
						"npm":  "@ai-sdk/openai-compatible",
						"name": "pluxy",
						"options": map[string]string{
							"baseURL": v1,
							"apiKey":  key,
						},
						"models": map[string]any{
							"<model-id>": map[string]string{
								"name": "<model-id>",
							},
						},
					},
				},
				"model": "pluxy/<model-id>",
			}, "", "  ")
			return string(b)
		},
	},
	{
		id:     "pi",
		name:   "Pi",
		config: "~/.pi/agent/cliproxyapi.json",
		render: func(baseURL, key string) string {
			root := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
			b, _ := json.MarshalIndent(map[string]string{
				"baseUrl": root,
				"apiKey":  key,
			}, "", "  ")
			return string(b) + "\n\n# Also register the pluxy-cliproxyapi-provider adapter in ~/.pi/agent/settings.json (see the desktop app)."
		},
	},
	{
		id:     "omp",
		name:   "Oh My Pi",
		config: "~/.omp/agent/models.yml",
		render: func(baseURL, key string) string {
			v1 := strings.TrimRight(baseURL, "/") + "/v1"
			return fmt.Sprintf(`# ~/.omp/agent/models.yml
providers:
  pluxy:
    baseUrl: %s
    apiKey: %s
    api: openai-responses
    discovery:
      type: openai-models-list

# ~/.omp/agent/config.yml
modelRoles:
  default: pluxy/<model-id>`, v1, key)
		},
	},
}

func lookupClientShow(tool string) (clientShowSpec, bool) {
	norm := strings.ToLower(strings.TrimSpace(tool))
	switch norm {
	case "claude-code", "pluxy/claude-code":
		return clientShowTable[0], true
	case "codex", "codex-cli", "pluxy/codex-setup":
		return clientShowTable[1], true
	case "opencode", "pluxy/opencode":
		return clientShowTable[2], true
	case "pi", "pluxy/pi-setup":
		return clientShowTable[3], true
	case "omp", "oh-my-pi", "pluxy/omp-setup":
		return clientShowTable[4], true
	default:
		return clientShowSpec{}, false
	}
}

func runClientShow(x *invocation) error {
	spec, ok := lookupClientShow(x.args[0])
	if !ok {
		return usageError{fmt.Sprintf("unknown client tool %q (supported: claude-code, codex, opencode, pi, omp)", x.args[0])}
	}
	if _, err := x.client.Setup(x.ctx); err != nil {
		return err
	}
	keyPlaceholder := "<client-key>"
	if x.opts.keyID != "" {
		keys, err := x.client.ClientKeys(x.ctx)
		if err != nil {
			return err
		}
		found := false
		for _, k := range keys {
			if k.ID == x.opts.keyID {
				keyPlaceholder = fmt.Sprintf("<client-key:%s (%s)>", k.ID, k.Masked)
				found = true
				break
			}
		}
		if !found {
			return &management.APIError{
				Status:  http.StatusNotFound,
				Code:    "not_found",
				Message: fmt.Sprintf("no client key with id %q", x.opts.keyID),
			}
		}
	}
	baseURL := strings.TrimRight(x.client.URL, "/")
	content := spec.render(baseURL, keyPlaceholder)
	if x.json {
		out := struct {
			Tool    string `json:"tool"`
			Name    string `json:"name"`
			Config  string `json:"config"`
			BaseURL string `json:"base_url"`
			Key     string `json:"key"`
			Content string `json:"content"`
		}{
			Tool:    spec.id,
			Name:    spec.name,
			Config:  spec.config,
			BaseURL: baseURL,
			Key:     keyPlaceholder,
			Content: content,
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		x.printf("%s\n", b)
		return nil
	}
	x.printf("Client: %s\nConfig: %s\nAddress: %s\n\n%s\n", spec.name, spec.config, baseURL, content)
	return nil
}
