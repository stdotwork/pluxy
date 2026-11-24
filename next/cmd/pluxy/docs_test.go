package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The docs are read by this file's tests, which check that what they name exists: a pluxy
// command and the flags written after it, a PLUXY_ variable, a file or folder of the repository,
// and the README's quick start. A test here fails with the doc, the line and what to change.

// TestDocs checks that the docs name only what exists. When a subtest fails, it says the doc and
// the line: change the doc (or the code, if the doc is right).
func TestDocs(t *testing.T) {
	t.Run("commands", docsCommands)
	t.Run("env vars", docsEnvVars)
	t.Run("paths and links", docsPaths)
	t.Run("quick start", docsQuickStart)
}

// docFiles are the docs the tests read, from the repository's root.
var docFiles = []string{
	"README.md", "AGENTS.md", "next/README.md", "next/management/README.md", "next/ui/README.md",
	"next/app/README.md", "next/protocol/README.md", "next/corpus/README.md", "next/app-tools/README.md",
	// docs/*.md are added by readDocs.
}

// docLine is one line of a doc, with whether it is inside a fenced block and that block's
// language (the word after the three backticks, empty when there is none).
type docLine struct {
	doc   string // slash-separated path from the repository root
	n     int    // 1-based
	text  string
	fence bool
	lang  string
}

// repoRoot is the repository's root: go test runs in the package's folder, next/cmd/pluxy.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(wd, "..", "..", "..")
	for _, f := range []string{"AGENTS.md", filepath.Join("next", "go.mod")} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Fatalf("%s is not the repository's root (no %s): this test finds it from next/cmd/pluxy", root, f)
		}
	}
	return root
}

// readDocs returns every line of the docs, fenced or not.
func readDocs(t *testing.T, root string) []docLine {
	t.Helper()
	docs := slices.Clone(docFiles)
	more, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range more {
		docs = append(docs, "docs/"+filepath.Base(m))
	}
	var lines []docLine
	for _, d := range docs {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(d)))
		if err != nil {
			t.Fatalf("%s is read by the docs tests (docFiles in next/cmd/pluxy/docs_test.go) and cannot be opened: %v; if the doc moved, change docFiles", d, err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		fence, lang, mark := false, "", ""
		for n := 1; sc.Scan(); n++ {
			text := sc.Text()
			trimmed := strings.TrimSpace(text)
			if fence {
				if strings.HasPrefix(trimmed, mark) && strings.Trim(trimmed, mark[:1]) == "" {
					fence = false
					continue
				}
			} else if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				mark = trimmed[:3]
				fence, lang = true, strings.TrimSpace(strings.TrimLeft(trimmed, mark[:1]))
				continue
			}
			lines = append(lines, docLine{doc: d, n: n, text: text, fence: fence, lang: lang})
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
	}
	if len(lines) < 1000 {
		t.Fatalf("read %d doc lines: fewer than the docs hold, so the docs are not read whole", len(lines))
	}
	return lines
}

var codeSpan = regexp.MustCompile("`+([^`]+)`+")

// codeTexts returns the pieces of a line a reader would type or look up as written: every
// inline code span of a prose line, or the whole line of a fenced block in a shell language.
func codeTexts(l docLine) []string {
	if l.fence {
		if slices.Contains([]string{"bash", "sh", "shell", "console", "zsh"}, l.lang) {
			return []string{strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l.text), "$ "))}
		}
		return nil
	}
	var out []string
	for _, m := range codeSpan.FindAllStringSubmatch(l.text, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// pluxyCommandPos matches pluxy as a program name: the word itself, or ./pluxy, ./pluxy.exe,
// bin/pluxy, followed by a space. pluxy/claude (a provider's ID) and pluxy-pack are not matched.
var pluxyCommandPos = regexp.MustCompile(`(?:^|[\s(;&|])(?:\.{1,2}[/\\]|\./cmd/)?pluxy(?:\.exe)?[ \t]+(\S.*)$`)

var (
	wordToken = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	flagToken = regexp.MustCompile(`^--?[A-Za-z][A-Za-z0-9-]*(=.*)?$`)
)

// pluxyUse is one `pluxy ...` written in a doc: the words that follow it, up to the first thing
// that is not a plain lower-case word, and the flags that follow those.
type pluxyUse struct {
	words, flags []string
}

// parsePluxyUse reads the command a text types, if it types one. A text that reaches the
// program only through a pipe, a redirect or a second command stops there.
func parsePluxyUse(text string) (pluxyUse, bool) {
	m := pluxyCommandPos.FindStringSubmatch(text)
	if m == nil {
		return pluxyUse{}, false
	}
	var u pluxyUse
	for _, tok := range strings.Fields(m[1]) {
		if strings.HasPrefix(tok, "(") || strings.HasSuffix(tok, ",") {
			return pluxyUse{}, false // pluxy's own output quoted in a sentence (pluxy dev (no commit ...)), not a command
		}
		if slices.Contains([]string{"|", "||", "&&", ";", ">", ">>", "<", "&", "#"}, tok) || strings.HasPrefix(tok, "#") || strings.HasPrefix(tok, "2>") {
			break
		}
		tok = strings.Trim(tok, "[]")
		switch {
		case flagToken.MatchString(tok):
			u.flags = append(u.flags, strings.TrimLeft(strings.SplitN(tok, "=", 2)[0], "-"))
		case wordToken.MatchString(tok) && len(u.flags) == 0:
			u.words = append(u.words, tok)
		}
	}
	return u, len(u.words) > 0
}

// cliFlags are the flags of every client command, which runCLIWithInput defines for all of them.
// help (-h, -help) is the flag package's own.
var cliFlags = []string{"json", "config", "url", "key", "h", "help"}

// commandFlags returns the names of the flags a client command takes.
func commandFlags(cmd *command) []string {
	fs := flag.NewFlagSet(cmd.words, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if cmd.flags != nil {
		var o opts
		cmd.flags(fs, &o)
	}
	names := slices.Clone(cliFlags)
	fs.VisitAll(func(f *flag.Flag) { names = append(names, f.Name) })
	return names
}

// plannedCommands are commands the management README's Resources tables name before they exist
// (laterCommands in cli_test.go lists the calls behind them). The list only shrinks: a command
// added to the CLI comes out of it.
var plannedCommands = []string{"config validate", "config apply", "key rotate-management", "ext config"}

// checkPluxyUse returns what is wrong with a `pluxy ...` a doc writes, or "".
//
// pluxy serve and pluxy init define their flags inside their functions (serve, initConfig), where
// a test cannot read them: their flags are not checked. A client command's flags are, and so is a
// word after a command that takes no arguments (pluxy status now).
func checkPluxyUse(u pluxyUse) string {
	switch u.words[0] {
	case "serve", "init", "help":
		return ""
	}
	cmd, rest := find(u.words)
	if cmd == nil {
		for _, p := range plannedCommands {
			if strings.HasPrefix(strings.Join(u.words, " "), p) {
				return ""
			}
		}
		if groupPrefix(u.words[0]) != "" {
			return fmt.Sprintf("pluxy %s is not a command: %s", strings.Join(u.words[:min(len(u.words), 2)], " "), commandHint(u.words[0]))
		}
		return fmt.Sprintf("pluxy %s is not a command: %s", u.words[0], commandHint(""))
	}
	if len(cmd.args) == 0 && len(rest) > 0 {
		return fmt.Sprintf("pluxy %s takes no argument, so %q after it is not a command: %s", cmd.words, rest[0], commandHint(u.words[0]))
	}
	known := commandFlags(cmd)
	for _, f := range u.flags {
		if !slices.Contains(known, f) {
			return fmt.Sprintf("pluxy %s has no flag -%s; it has %s: change the doc to a flag it has", cmd.words, f, strings.Join(known, ", "))
		}
	}
	return ""
}

// commandHint lists what the doc could have meant: the commands of a group, or all the first words.
func commandHint(group string) string {
	var out []string
	for _, c := range commands {
		w := strings.Fields(c.words)
		switch {
		case group == "" && !slices.Contains(out, w[0]):
			out = append(out, w[0])
		case group != "" && w[0] == group:
			out = append(out, c.words)
		}
	}
	if group == "" {
		out = append(out, "serve", "init", "help")
		return "the commands are " + strings.Join(out, ", ") + " (pluxy help lists them); name one of them or take the line out"
	}
	return "the commands in the group are " + strings.Join(out, ", ") + "; name one of them or take the line out"
}

func docsCommands(t *testing.T) {
	root := repoRoot(t)
	for _, p := range plannedCommands {
		if cmd, _ := find(strings.Fields(p)); cmd != nil && cmd.words == p {
			t.Errorf("pluxy %s exists now: take it out of plannedCommands in next/cmd/pluxy/docs_test.go", p)
		}
	}
	checked := 0
	for _, l := range readDocs(t, root) {
		for _, text := range codeTexts(l) {
			u, ok := parsePluxyUse(text)
			if !ok {
				continue
			}
			checked++
			if msg := checkPluxyUse(u); msg != "" {
				t.Errorf("%s:%d: %s\n\tin: %s", l.doc, l.n, msg, text)
			}
		}
	}
	if checked < 40 {
		t.Errorf("checked %d pluxy commands in the docs, fewer than today's 100 or so: the docs are not read whole", checked)
	}
}

var envName = regexp.MustCompile(`PLUXY_[A-Z0-9_]*[A-Z0-9]`)

// undocumentedEnv are the PLUXY_ variables shipped Go code reads and no doc names. The list may
// only shrink: a variable added to the code is named in a doc, or in the doc that is its home.
var undocumentedEnv = map[string]string{
	// next/cmd/pluxy-bench, the benchmark program docs/perf.md's numbers come from.
	"PLUXY_BENCH_PROFILE": "", "PLUXY_BENCH_PROFILE_SECONDS": "", "PLUXY_BENCH_PROFILE_STOP": "",
	// next/internal/catalogue/gen.go, the model catalogue's generator.
	"PLUXY_MODELS_DEV_URL": "",
	// next/internal/core/routingstate.go, how often the block check runs, in a test.
	"PLUXY_TEST_CHECK_INTERVAL_MS": "",
}

// envScanSkip and envScanExt say what the scan for where a variable is read goes through:
// archived code (next/archive), dependencies and build output are not scanned.
var envScanSkip = []string{".git", "node_modules", "target", "dist", ".private", "archive"}
var envScanExt = []string{".go", ".rs", ".ts", ".tsx", ".js", ".mjs", ".cjs", ".yml", ".yaml"}

// shippedEnvDirs are where code that ships (not tests, e2e, the corpus or the conformance kit) is.
var shippedEnvDirs = []string{"next/cmd", "next/internal", "next/sdk", "next/extensions"}

var quotedEnv = regexp.MustCompile(`"(PLUXY_[A-Z0-9_]*[A-Z0-9])"`)

func docsEnvVars(t *testing.T) {
	root := repoRoot(t)
	inCode := map[string]bool{}  // variables that appear in any code file, as written
	shipped := map[string]bool{} // variables a shipped Go file reads, as a string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if slices.Contains(envScanSkip, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !slices.Contains(envScanExt, filepath.Ext(p)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(b), "PLUXY_") {
			return nil
		}
		for _, n := range envName.FindAllString(string(b), -1) {
			inCode[n] = true
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") && slices.ContainsFunc(shippedEnvDirs, func(dir string) bool { return strings.HasPrefix(rel, dir+"/") }) {
			for _, m := range quotedEnv.FindAllStringSubmatch(string(b), -1) {
				shipped[m[1]] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inCode) < 10 || len(shipped) < 3 {
		t.Fatalf("found %d variables in code and %d in shipped Go code: the scan does not read the tree whole", len(inCode), len(shipped))
	}
	documented := map[string]bool{}
	for _, l := range readDocs(t, root) {
		for _, n := range envName.FindAllString(l.text, -1) {
			documented[n] = true
			if !inCode[n] {
				t.Errorf("%s:%d: %s is read nowhere in the code (Go, Rust, TypeScript, workflows): the variable was renamed or removed; name the one the code reads, or take the line out", l.doc, l.n, n)
			}
		}
	}
	for n := range shipped {
		_, allowed := undocumentedEnv[n]
		switch {
		case !documented[n] && !allowed:
			t.Errorf("shipped code reads %s and no doc names it: add it to the doc that lists the variables it belongs with (next/README.md for core's), or to undocumentedEnv in next/cmd/pluxy/docs_test.go, a list that may only shrink", n)
		case documented[n] && allowed:
			t.Errorf("%s is named in a doc now: take it out of undocumentedEnv in next/cmd/pluxy/docs_test.go", n)
		}
	}
	for n := range undocumentedEnv {
		if !shipped[n] {
			t.Errorf("undocumentedEnv names %s, which shipped code does not read: take it out", n)
		}
	}
}

var (
	mdLink     = regexp.MustCompile(`!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	repoPath   = regexp.MustCompile(`^[A-Za-z0-9_.@-]+(/[A-Za-z0-9_.@-]+)+/?$`)
	hasFileExt = regexp.MustCompile(`\.[A-Za-z0-9]{1,6}$`)
)

// pathSkips are paths that start in a folder of the repository and still name something else, such
// as a file of another project's repository that a doc describes. Each has the reason.
var pathSkips = map[string]string{
	"extensions/panel/src/features/quota/": "the old repository's, at a recorded commit (next/corpus/README.md)",
	// Other projects' files, which next/app-tools/README.md reads the settings of.
	"docs/models.md": "Pi's", "docs/settings.md": "Pi's", "docs/packages.md": "Pi's", "docs/quickstart.md": "Pi's",
	"docs/user-guide/26-config-reference.md": "Grok Build's",
}

// docsPaths checks Markdown links, and backticked paths that start in a folder of the
// repository (so next/internal/route/pick.go, docs/ci.md, or ui/src/x.ts from next/README.md).
// A path whose first folder is none of the repository's (state/routing.json, which a running
// pluxy makes; codex-rs/..., another project's) is skipped, and so are addresses.
func docsPaths(t *testing.T) {
	root := repoRoot(t)
	checked, skipped := 0, 0
	for _, l := range readDocs(t, root) {
		if l.fence {
			continue
		}
		docDir := filepath.Join(root, filepath.FromSlash(path.Dir(l.doc)))
		for _, m := range mdLink.FindAllStringSubmatch(l.text, -1) {
			target := strings.Trim(m[1], "<>")
			if i := strings.IndexAny(target, "#?"); i >= 0 {
				target = target[:i]
			}
			if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") || strings.ContainsAny(target, "*{}$\\") {
				skipped++
				continue
			}
			checked++
			// A link is relative to the doc that holds it.
			if _, err := os.Stat(filepath.Join(docDir, filepath.FromSlash(target))); err != nil {
				t.Errorf("%s:%d: the link (%s) points at nothing: the file or folder was moved or removed; point it at where it is now, or take the link out", l.doc, l.n, m[1])
			}
		}
		for _, p := range codeTexts(l) {
			if !repoPath.MatchString(p) || strings.Contains(p, "...") {
				continue
			}
			trailing := strings.HasSuffix(p, "/")
			if !trailing && !hasFileExt.MatchString(p) {
				continue
			}
			rel := filepath.FromSlash(strings.TrimSuffix(p, "/"))
			first, _, _ := strings.Cut(p, "/")
			// The bases a doc writes a path from: the repository's root, its own folder, next/.
			anchored, found := false, false
			for _, base := range []string{root, docDir, filepath.Join(root, "next")} {
				if st, err := os.Stat(filepath.Join(base, first)); err != nil || !st.IsDir() {
					continue
				}
				anchored = true
				if _, err := os.Stat(filepath.Join(base, rel)); err == nil {
					found = true
				}
			}
			// A doc also writes a path from the folder it is about, deeper than its own (tests/modes.rs
			// for next/app/src-tauri/tests/modes.rs): look below its folder for a path that ends so.
			if anchored && !found {
				found = endsPath(docDir, rel)
			}
			switch {
			case pathSkips[p] != "" || !anchored:
				skipped++
			case found:
				checked++
			default:
				checked++
				t.Errorf("%s:%d: `%s` is not a file or folder of the repository (looked from the repository's root, the doc's folder and next/): it was moved or removed; name where it is now, or take it out", l.doc, l.n, p)
			}
		}
	}
	if checked < 100 {
		t.Errorf("checked %d links and paths, fewer than today's several hundred: the docs are not read whole", checked)
	}
	t.Logf("checked %d links and paths, skipped %d (addresses, URLs, globs, placeholders, paths that start outside the repository's folders)", checked, skipped)
}

var (
	goTarget  = regexp.MustCompile(`^\./[A-Za-z0-9_./-]+$`)
	buildOut  = "-o"
	quickHead = "## Quick start"
)

// docsQuickStart reads the quick start's shell commands without running them: the
// folders it moves to and the Go packages it runs or builds exist, the programs it starts are ones
// an earlier line built, and the pluxy commands are real (docsCommands).
func docsQuickStart(t *testing.T) {
	root := repoRoot(t)
	var qs []docLine
	in := false
	for _, l := range readDocs(t, root) {
		if l.doc != "README.md" {
			continue
		}
		if !l.fence && strings.HasPrefix(l.text, "## ") {
			in = l.text == quickHead
		}
		if in && l.fence && l.lang == "bash" {
			qs = append(qs, l)
		}
	}
	if len(qs) < 5 {
		t.Fatalf("found %d shell lines in README.md's %q section: the section was renamed or its commands are no longer fenced as bash; update quickHead in next/cmd/pluxy/docs_test.go", len(qs), quickHead)
	}
	// Join continued lines (a trailing backslash) into one command.
	type cmdLine struct {
		n    int
		text string
	}
	var cmds []cmdLine
	for i := 0; i < len(qs); i++ {
		text := strings.TrimSpace(qs[i].text)
		n := qs[i].n
		for strings.HasSuffix(text, `\`) && i+1 < len(qs) {
			i++
			text = strings.TrimSpace(strings.TrimSuffix(text, `\`)) + " " + strings.TrimSpace(qs[i].text)
		}
		cmds = append(cmds, cmdLine{n, text})
	}
	cwd := ""                   // slash path from the repository's root; "?" outside the repository
	cloned := map[string]bool{} // folders git clone made, standing for the root
	built := map[string]bool{}  // programs a go build -o made
	for _, c := range cmds {
		bad := func(format string, a ...any) {
			t.Errorf("README.md:%d: %s\n\tin: %s", c.n, fmt.Sprintf(format, a...), c.text)
		}
		fields := strings.Fields(c.text)
		switch {
		case len(fields) == 0:
		case fields[0] == "git" && len(fields) >= 3 && fields[1] == "clone":
			dir := path.Base(strings.TrimSuffix(fields[2], ".git"))
			if len(fields) > 3 {
				dir = fields[3]
			}
			cloned[dir] = true
			cwd = "?"
		case fields[0] == "cd" && len(fields) == 2:
			dir := strings.TrimSuffix(fields[1], "/")
			head, rest, _ := strings.Cut(dir, "/")
			switch {
			case cloned[head]:
				cwd = path.Clean(rest)
				if cwd == "." {
					cwd = ""
				}
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(cwd))); err != nil {
					bad("cd %s: %s is not a folder of the repository: the repository's layout changed; change the cd", dir, cwd)
				}
			case !strings.HasPrefix(dir, "~") && !strings.HasPrefix(dir, "/") && cwd != "?":
				cwd = path.Join(cwd, dir)
				if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(cwd))); err != nil {
					bad("cd %s: %s is not a folder of the repository: change the cd", dir, cwd)
				}
			default:
				cwd = "?" // a folder pluxy's user made, outside the repository
			}
		case fields[0] == "go" && len(fields) >= 3 && slices.Contains([]string{"run", "build", "generate"}, fields[1]):
			out, pkg := "", ""
			var flags []string
			for i := 2; i < len(fields); i++ {
				switch {
				case fields[i] == buildOut && i+1 < len(fields):
					out = fields[i+1]
					i++
				case goTarget.MatchString(fields[i]) && pkg == "":
					pkg = fields[i]
				case strings.HasPrefix(fields[i], "-") && pkg != "":
					flags = append(flags, strings.TrimLeft(strings.SplitN(fields[i], "=", 2)[0], "-"))
				}
			}
			if cwd == "?" {
				bad("go %s runs outside the repository: cd into it first (the quick start clones it, then cd's into next)", fields[1])
				break
			}
			if pkg == "" {
				bad("go %s names no package starting ./", fields[1])
				break
			}
			dir := filepath.Join(root, filepath.FromSlash(cwd), filepath.FromSlash(pkg))
			goFiles, _ := filepath.Glob(filepath.Join(dir, "*.go"))
			if len(goFiles) == 0 {
				bad("go %s %s: %s holds no Go package: the package was moved or renamed; name where it is now", fields[1], pkg, path.Join(cwd, pkg))
				break
			}
			if out != "" {
				built[strings.TrimSuffix(path.Base(filepath.ToSlash(out)), ".exe")] = true
			}
			if fields[1] == "run" {
				src := ""
				for _, f := range goFiles {
					b, _ := os.ReadFile(f)
					src += string(b)
				}
				for _, f := range flags {
					if !strings.Contains(src, `"`+f+`"`) {
						bad("go run %s -%s: no flag of that name in %s: change the doc to a flag the program has", pkg, f, path.Join(cwd, pkg))
					}
				}
			}
		case fields[0] == "curl":
		default:
			prog := strings.TrimPrefix(fields[0], "./")
			if (prog == "pluxy" || prog == "pluxy.exe") && !built["pluxy"] {
				bad("%s runs a program no earlier line built: add the go build -o ... ./cmd/pluxy line before it", fields[0])
			}
		}
		for _, u := range pluxyUses(c.text) {
			if msg := checkPluxyUse(u); msg != "" {
				bad("%s", msg)
			}
		}
	}
	if !built["pluxy"] {
		t.Errorf("README.md's quick start never builds pluxy (go build -o <folder>/pluxy ./cmd/pluxy), or the line is not in a bash block of %q", quickHead)
	}
}

// pluxyUses reads every pluxy command a command line types, one per && or ; part.
func pluxyUses(text string) []pluxyUse {
	var out []pluxyUse
	for _, part := range strings.FieldsFunc(strings.NewReplacer("&&", ";", "|", ";").Replace(text), func(r rune) bool { return r == ';' }) {
		if u, ok := parsePluxyUse(strings.TrimSpace(part)); ok {
			out = append(out, u)
		}
	}
	return out
}

// endsPath reports whether a file or folder below dir has a path that ends in rel.
func endsPath(dir, rel string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil || found:
			return filepath.SkipAll
		case d.IsDir() && slices.Contains(envScanSkip, d.Name()):
			return filepath.SkipDir
		case strings.HasSuffix(p, string(filepath.Separator)+rel):
			found = true
		}
		return nil
	})
	return found
}
