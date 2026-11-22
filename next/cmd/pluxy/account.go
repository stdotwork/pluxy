package main

// The account commands that add an account, sign in to one again, remove it, renew its
// credential, change its routing settings, import a credential file and show its plan. Adding
// one is a sign-in core runs: the command starts it, shows what the sign-in asks for, sends what
// the person types, and reads it again until it ends, as the panel's add-account page does
// (next/ui/kit/loginFlow.tsx).

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stdotwork/pluxy/next/internal/cred"
	"github.com/stdotwork/pluxy/next/management"
	"github.com/stdotwork/pluxy/next/protocol"
)

// accountLoginWait is how often a sign-in in flight is read again: the panel asks every 2
// seconds too. A test sets it shorter for a sign-in it makes core fail.
var accountLoginWait = 2 * time.Second

// accountLoginAttempts is how many reads of a sign-in may fail in a row before the command gives
// up on it: the panel tries three times (next/ui/kit/loginFlow.tsx).
const accountLoginAttempts = 3

// accountAnswerWait is how long a sign-in may go on showing the prompt it was answered on once
// standard input has nothing more to give: an extension may still be checking that answer, and a
// wrong one leaves the same prompt up for ever. A test sets it shorter.
var accountAnswerWait = time.Minute

// accountLoginTimeout bounds a sign-in the command follows, so a provider that never ends one
// does not hold the terminal for ever. A sign-in's own prompt expires long before.
const accountLoginTimeout = 30 * time.Minute

// waitingError ends a command that started something a person must finish elsewhere, such as a
// sign-in that needs a browser: exit 7 (next/management/README.md).
type waitingError struct{ msg string }

func (e waitingError) Error() string { return e.msg }

// inputFlag collects the --input name=value pairs: what the person entered for the inputs a
// sign-in asks for. A secret is refused here (a flag stands in the shell's history and in the
// process list) and read from a prompt or standard input instead. A pair that is not name=value
// is kept as bad and refused once the flags are read: the flag package puts the value it was
// given into its own error, and a value given here may turn out to be secret.
type inputFlag struct {
	values map[string]string
	bad    error
}

// String writes no values. The flag package prints them in help and quotes them in its errors,
// and its error is the one thing a bad pair would otherwise put them into.
func (f *inputFlag) String() string { return "" }

func (f *inputFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		if f.bad == nil {
			f.bad = errors.New("write it as name=value")
		}
		return nil
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[name] = value
	return nil
}

// loginMethods is GET /login-methods: what each running extension can sign in with.
func (x *invocation) loginMethods() (management.LoginMethodsReply, error) {
	var r management.LoginMethodsReply
	_, err := x.client.Call(x.ctx, http.MethodGet, "/login-methods", nil, nil, &r)
	return r, err
}

// methodsFor is the extension named by id and its login methods.
func methodsFor(r management.LoginMethodsReply, id string) (*management.LoginMethods, error) {
	var have []string
	for i := range r.Extensions {
		if r.Extensions[i].Extension == id {
			return &r.Extensions[i], nil
		}
		have = append(have, r.Extensions[i].Extension)
	}
	if len(have) == 0 {
		return nil, usageError{"no extension offers a sign-in: install one with pluxy ext install (pluxy ext list shows what is installed)"}
	}
	return nil, usageError{fmt.Sprintf("no extension %q offers a sign-in; these do: %s", id, strings.Join(have, ", "))}
}

// pickMethod is the method named by want, or the only one the extension offers. account names the
// account signing in again, if any: core keeps no record of the way it signed in before, so the
// command cannot choose for it.
func pickMethod(m *management.LoginMethods, want, account string) (protocol.LoginMethod, error) {
	if want != "" {
		for _, method := range m.Methods {
			if method.Method == want {
				return method, nil
			}
		}
		var names []string
		for _, method := range m.Methods {
			names = append(names, method.Method)
		}
		if len(names) == 0 {
			return protocol.LoginMethod{}, usageError{fmt.Sprintf("%s has no sign-in methods", m.Extension)}
		}
		return protocol.LoginMethod{}, usageError{fmt.Sprintf("%s has no sign-in method %q; it has %s", m.Extension, want, strings.Join(names, ", "))}
	}
	switch len(m.Methods) {
	case 0:
		return protocol.LoginMethod{}, usageError{fmt.Sprintf("%s has no sign-in methods", m.Extension)}
	case 1:
		return m.Methods[0], nil
	}
	var labels []string
	for _, method := range m.Methods {
		label := method.Method
		if method.Label != "" {
			label += " (" + method.Label + ")"
		}
		labels = append(labels, label)
	}
	unrecorded := ""
	if account != "" {
		unrecorded = fmt.Sprintf(", and core does not record which one account %s used", account)
	}
	return protocol.LoginMethod{}, usageError{fmt.Sprintf("%s signs in %d ways%s; name one with --method: %s", m.Extension, len(m.Methods), unrecorded, strings.Join(labels, ", "))}
}

// signInFlags adds the flags account add and account login share: the sign-in method, what the
// person entered, and whether to follow the sign-in at all.
func signInFlags(fs *flag.FlagSet, o *opts) {
	fs.StringVar(&o.method, "method", "", "the sign-in method (default: the extension's only one)")
	fs.Var(&o.input, "input", "one value the sign-in asks for, name=value (may be repeated; a secret is read from a prompt or standard input instead)")
	fs.BoolVar(&o.noWait, "no-wait", false, "print the sign-in's address or code and stop, leaving it to finish in core")
}

// newAccountID is a fresh ID for an account the command adds, as the panel makes one:
// account- and 12 hex digits.
func newAccountID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "account"
	}
	return "account-" + hex.EncodeToString(b[:])
}

// runAccountMethods is pluxy account methods: how each running extension can add an account.
func runAccountMethods(x *invocation) error {
	var r management.LoginMethodsReply
	return x.call(http.MethodGet, "/login-methods", nil, nil, &r, func() {
		if len(r.Extensions) == 0 {
			x.printf("no extension offers a sign-in: install one with pluxy ext install\n")
			return
		}
		rows := [][]string{{"EXTENSION", "METHOD", "KIND", "LABEL", "INPUTS"}}
		for _, ext := range r.Extensions {
			for _, m := range ext.Methods {
				rows = append(rows, []string{ext.Extension, m.Method, m.Kind, dash(m.Label), inputsText(m.Inputs)})
			}
		}
		x.table(rows)
	})
}

// inputsText writes what a sign-in asks the person for: each input's name, marked when it is
// secret (never shown again) or may be left empty.
func inputsText(inputs []protocol.LoginInput) string {
	if len(inputs) == 0 {
		return "-"
	}
	names := make([]string, len(inputs))
	for i, in := range inputs {
		name := in.Name
		if in.Secret {
			name += " (secret)"
		}
		if in.Optional {
			name += " (optional)"
		}
		names[i] = name
	}
	return strings.Join(names, ", ")
}

// runAccountAdd is pluxy account add <provider>: it starts a sign-in and follows it to the end.
func runAccountAdd(x *invocation) error {
	provider := x.args[0]
	methods, err := x.loginMethods()
	if err != nil {
		return err
	}
	ext, err := methodsFor(methods, provider)
	if err != nil {
		return err
	}
	method, err := pickMethod(ext, x.opts.method, "")
	if err != nil {
		return err
	}
	id := x.opts.id
	if id == "" {
		id = newAccountID()
	}
	if !cred.ValidID(id) {
		return usageError{fmt.Sprintf("account ID %q is not valid: use 1 to 128 letters, digits, dots, underscores or hyphens, starting with a letter or digit and not ending with a dot (--id)", id)}
	}
	lines := newLineReader(x.stdin, x.terminal())
	defer lines.close()
	in := signInInputs{vals: maps.Clone(x.opts.input.values)}
	input, err := x.startInputs(method.Inputs, in, lines)
	if err != nil {
		return err
	}
	var reply management.LoginReply
	body, err := x.client.Call(x.ctx, http.MethodPost, "/accounts", nil, management.AddAccountRequest{
		ID: id, Extension: provider, Method: method.Method, Input: input,
	}, &reply)
	if err != nil {
		return err
	}
	if reply.Login.ID == "" {
		return errors.New("core started no sign-in")
	}
	return x.afterLoginStart(reply.Login, body, in, lines, false)
}

// runAccountLogin is pluxy account login <id>: it signs in to an account again.
func runAccountLogin(x *invocation) error {
	id := x.args[0]
	var accounts management.AccountsReply
	if _, err := x.client.Call(x.ctx, http.MethodGet, "/accounts", nil, nil, &accounts); err != nil {
		return err
	}
	i := slices.IndexFunc(accounts.Accounts, func(a management.Account) bool { return a.ID == id })
	if i < 0 {
		return &management.APIError{Status: http.StatusNotFound, Code: "not_found", Message: fmt.Sprintf("no account %q", id)}
	}
	ext := accounts.Accounts[i].Extension
	if ext == "" {
		return &management.APIError{Status: http.StatusConflict, Code: "conflict", Message: fmt.Sprintf("account %q is run by no extension", id)}
	}
	methods, err := x.loginMethods()
	if err != nil {
		return err
	}
	offered, err := methodsFor(methods, ext)
	if err != nil {
		return err
	}
	method, err := pickMethod(offered, x.opts.method, id)
	if err != nil {
		return err
	}
	lines := newLineReader(x.stdin, x.terminal())
	defer lines.close()
	// A sign-in again may leave empty what the account's settings already hold, as the panel
	// does: a non-secret input the account keeps is optional (next/ui/kit/loginFlow.tsx).
	in := signInInputs{vals: maps.Clone(x.opts.input.values), kept: settingsKeys(accounts.Accounts[i].Settings)}
	input, err := x.startInputs(method.Inputs, in, lines)
	if err != nil {
		return err
	}
	var reply management.LoginReply
	body, err := x.client.Call(x.ctx, http.MethodPost, accountPath(id)+"/login", nil, management.LoginAgainRequest{
		Method: method.Method, Input: input,
	}, &reply)
	if err != nil {
		return err
	}
	if reply.Login.ID == "" {
		return errors.New("core started no sign-in")
	}
	return x.afterLoginStart(reply.Login, body, in, lines, true)
}

// afterLoginStart ends the sign-in a command started: it says where to go and stops (--no-wait,
// exit 7) or follows it to its end. A sign-in that ended at once has nothing to wait for.
func (x *invocation) afterLoginStart(l management.Login, body []byte, in signInInputs, lines *lineReader, again bool) error {
	if x.opts.noWait && l.State == management.LoginPending {
		if l.Prompt != nil && l.Prompt.Kind == protocol.PromptInput {
			// A prompt's values are read by this command alone, so a sign-in core keeps waiting on
			// one never ends where it is: cancel it and say what to give.
			var names []string
			for _, f := range l.Prompt.Inputs {
				names = append(names, fieldName(f))
			}
			need := "a value"
			if len(names) > 0 {
				need = strings.Join(names, ", ")
			}
			return x.cancelLogin(l.ID, usageError{fmt.Sprintf("the sign-in waits for %s, and --no-wait leaves nothing to answer it: run it again without --no-wait", need)})
		}
		x.loginPrompt(l.Prompt)
		if x.json {
			if err := x.writeBody(body); err != nil {
				return err
			}
		}
		return waitingError{fmt.Sprintf("waiting for the sign-in for %s to finish: it goes on in core, and the account appears in pluxy account list when it does", l.Account)}
	}
	return x.followLogin(l, body, in, lines, again)
}

// followLogin reads a sign-in again until it ends, asking for what its prompts want. It alone
// decides which field is being answered and what the terminal's echo is: a line belongs to the
// field current when it is received here, and waiting for one never holds up the reads of the
// sign-in.
func (x *invocation) followLogin(l management.Login, body []byte, in signInInputs, lines *lineReader, again bool) error {
	ctx, stop := context.WithTimeout(x.ctx, accountLoginTimeout)
	defer stop()
	// The terminal's echo goes back on before the sign-in is cancelled in core, which may take
	// seconds to answer.
	x.restoreEcho = lines.show
	defer func() { x.restoreEcho = nil }()

	shown := ""
	var prompt *promptInput
	// answered says core has an answer to the prompt shown, and reshown that a later read still
	// shows that prompt, so a person at a terminal may answer it again. The read made right after
	// an answer does not count: the extension may not have updated its prompt yet.
	answered, reshown := false, false
	// ended says standard input has ended. giveUp is the bounded wait on a prompt that was answered
	// and is still up with nothing that may answer it, and unanswered the field it still wants.
	ended := false
	var giveUp <-chan time.Time
	var unanswered protocol.LoginInput
	// browserLeft says a browser prompt was answered by a pipe and is still up, so nothing reads
	// standard input until it changes; waitSaid that the notice about the end of input is written.
	browserLeft, waitSaid := false, false
	// failures counts reads of the sign-in in a row that failed, as the panel does: three and the
	// command gives up on the sign-in, waiting longer between each try.
	failures := 0
	timer := time.NewTimer(accountLoginWait)
	defer timer.Stop()
	for {
		if l.State != management.LoginPending {
			return x.loginEnded(l, body, again)
		}
		if key := promptKey(l.Prompt); l.Prompt != nil && (key != shown || reshown) {
			repeated := key == shown
			shown, answered, reshown, giveUp, browserLeft = key, false, false, nil, false
			var err error
			if prompt, err = x.newPromptInput(l.Prompt, in); err != nil {
				return x.cancelLogin(l.ID, err)
			}
			prompt.repeated = repeated
			// The echo is set first and what was typed for the old prompt dropped next, both before
			// the new prompt is on the screen: what is typed after reading it is its answer. Once
			// standard input has ended nothing will read a value, so echo is not turned off.
			if ended {
				lines.show()
			} else {
				lines.field(prompt.next())
			}
			lines.dropTyped()
			if repeated {
				x.eprintf("the sign-in is still asking; give the value again if the last one was wrong\n")
			}
			x.loginPrompt(l.Prompt)
		}
		var field *protocol.LoginInput
		if prompt != nil {
			field = prompt.next()
		}
		if ended {
			lines.show()
		} else {
			lines.field(field)
		}
		if prompt != nil && field == nil {
			// The prompt wants nothing more: send what a flag, its settings or standard input
			// gave, or, with nothing to send, wait below for the sign-in to finish by itself. The
			// prompt stays the one shown: core's extension may not have moved on yet, and another
			// line read now would answer the prompt that comes next with this one's value.
			answer := prompt
			prompt = nil
			if answer.answerable() {
				if err := x.sendInput(ctx, l.ID, answer.got); err != nil {
					// With standard input ended no other value can come, so the same answer would
					// only be refused again.
					if ended || !loginRetryable(ctx, err) {
						return x.cancelLogin(l.ID, err)
					}
					// A person may give the value again: the prompt is shown once more, with what
					// core said below it.
					msg := fmt.Sprintf("the sign-in did not take that (%v); give it again\n", err)
					if stderrIsTerminal(x.stderr) {
						_ = writeEscaped(x.stderr, []byte(msg))
					} else {
						x.eprintf("%s", msg)
					}
					shown = ""
					continue
				}
				answered = true
				timer.Reset(accountLoginWait)
				// The answer is in. Reading the sign-in now says at once whether it ended; a read
				// that fails is left to the next one, since the answer is already sent.
				if next, nextBody, err := x.readLogin(ctx, l.ID); err == nil {
					l, body = next, nextBody
				}
				continue
			}
		}
		// Lines are received only for a field, so piped lines wait for the field they are for.
		var input <-chan lineRead
		if field != nil {
			if err := x.asks(*field); err != nil {
				return x.cancelLogin(l.ID, err)
			}
			// Once standard input has ended, no prompt asks for a value: nothing can read one.
			if x.prompts() && !ended {
				prompt.ask(x)
			}
			lines.start()
			input = lines.lines
		}
		var inputEnd <-chan struct{}
		if browserLeft && !waitSaid {
			inputEnd = lines.finished
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return x.cancelLogin(l.ID, fmt.Errorf("the sign-in did not finish in %s", accountLoginTimeout))
			}
			return x.cancelLogin(l.ID, errors.New("sign-in cancelled"))
		case <-giveUp:
			return x.cancelLogin(l.ID, usageError{fmt.Sprintf("the sign-in still asks for %s after the value it was given: %s", fieldName(unanswered), inputHint(unanswered))})
		case <-inputEnd:
			// Lines read before the end may still answer a later prompt, so input has not ended for
			// that prompt yet.
			waitSaid = true
			x.eprintf("%s\n", browserWaitLine())
		case r := <-input:
			if !r.ok {
				if prompt.kind == protocol.PromptBrowser {
					if !waitSaid {
						waitSaid = true
						// On a terminal the cursor is still after the field's label.
						if x.prompts() && !ended {
							x.eprintf("\n")
						}
						x.eprintf("%s\n", browserWaitLine())
					}
				} else if lines.hidden() {
					x.eprintf("\n")
				}
				ended = true
				if prompt.repeated && len(prompt.done) == 0 {
					// Nothing new to answer a prompt that already has an answer: the extension may
					// still be working on it, so it gets a while to move on. A browser prompt can
					// also end by its callback, and stays until the sign-in's own limit.
					if prompt.kind == protocol.PromptInput {
						unanswered = *field
						giveUp = time.After(accountAnswerWait)
					}
					prompt = nil
					continue
				}
				if err := prompt.endOfInput(x, l.ID); err != nil {
					return err
				}
				continue
			}
			complete, err := prompt.take(r)
			if err != nil {
				return x.cancelLogin(l.ID, err)
			}
			if complete && lines.hidden() {
				x.eprintf("\n")
			}
		case <-timer.C:
			next, nextBody, err := x.readLogin(ctx, l.ID)
			switch {
			case err == nil:
				failures = 0
				if answered && promptKey(next.Prompt) == shown {
					// A person can see the prompt is still up and answer it again. A piped line
					// never does: it may be the next prompt's value, perhaps a secret, so such a
					// sign-in gets a bounded wait to move on.
					if x.prompts() {
						reshown = true
					} else if next.Prompt.Kind == protocol.PromptBrowser {
						// The browser's return may still end it, so it is not given up on.
						browserLeft = true
					} else if giveUp == nil && next.Prompt.Kind == protocol.PromptInput && len(next.Prompt.Inputs) > 0 {
						unanswered = next.Prompt.Inputs[0]
						giveUp = time.After(accountAnswerWait)
					}
				}
				l, body = next, nextBody
				timer.Reset(accountLoginWait)
			case !loginRetryable(ctx, err) || failures+1 >= accountLoginAttempts:
				return x.cancelLogin(l.ID, fmt.Errorf("the sign-in could not be read: %w", err))
			default:
				failures++
				msg := fmt.Sprintf("could not read the sign-in (%v); trying again\n", err)
				if stderrIsTerminal(x.stderr) {
					_ = writeEscaped(x.stderr, []byte(msg))
				} else {
					x.eprintf("%s", msg)
				}
				timer.Reset(accountLoginWait << failures)
			}
		}
	}
}

// signInInputs is what a command already has for a sign-in: the values --input gave, by name, and
// the settings the account keeps, by name (empty for an account being added).
type signInInputs struct {
	vals map[string]string
	kept map[string]bool
}

// optionalInput is the panel's rule for a field that may be left empty (next/ui/kit/loginFlow.tsx
// optionalInput): the method says so, or a sign-in again finds the value already in the account's
// settings, where only a non-secret value is kept.
func optionalInput(f protocol.LoginInput, kept map[string]bool) bool {
	return f.Optional || (!f.Secret && kept[f.Name])
}

// settingsKeys names the settings an account keeps as they are stored, each of which a sign-in
// again may leave empty.
func settingsKeys(raw json.RawMessage) map[string]bool {
	keys := map[string]bool{}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return keys
	}
	for k := range m {
		keys[k] = true
	}
	return keys
}

// promptInput gathers what one prompt of a sign-in asks for.
type promptInput struct {
	kind   string
	fields []protocol.LoginInput
	kept   map[string]bool
	// got is what is sent back: the values that have one. done is every field answered, which an
	// optional field answered with nothing is too.
	got   map[string]string
	done  map[string]bool
	asked map[string]bool
	eof   bool
	// value is the field's value while it is not complete yet: a JSON document, such as a
	// pretty-printed service account key, spans several lines.
	value *inputValue
	// repeated says this is a prompt already answered once, shown again.
	repeated bool
}

// newPromptInput is the gathering for p: each value a flag gave is taken, except a secret's,
// which no flag may give.
func (x *invocation) newPromptInput(p *protocol.Prompt, in signInInputs) (*promptInput, error) {
	g := &promptInput{kind: p.Kind, fields: p.Inputs, kept: in.kept,
		got: map[string]string{}, done: map[string]bool{}, asked: map[string]bool{}}
	for _, f := range p.Inputs {
		v, ok := in.vals[f.Name]
		if !ok {
			continue
		}
		if f.Secret {
			return nil, usageError{fmt.Sprintf("%s is secret: the sign-in reads it from a prompt or standard input, never from --input", fieldName(f))}
		}
		g.got[f.Name] = v
		g.done[f.Name] = true
		delete(in.vals, f.Name)
	}
	return g, nil
}

// next is the field whose value the sign-in still waits for, or nil when it waits for no more.
func (g *promptInput) next() *protocol.LoginInput {
	if g.eof {
		return nil
	}
	for i := range g.fields {
		if !g.done[g.fields[i].Name] {
			return &g.fields[i]
		}
	}
	return nil
}

// answerable says whether the prompt waits for an answer this command can send: it asked for
// values, and standard input did not end before it had them.
func (g *promptInput) answerable() bool { return len(g.fields) > 0 && !g.eof }

// ask writes the label of the field standard input is about to read, once for each field.
func (g *promptInput) ask(x *invocation) {
	f := g.next()
	if f == nil || g.asked[f.Name] {
		return
	}
	g.asked[f.Name] = true
	x.eprintf("%s: ", x.errCell(fieldName(*f)))
}

// maxInputValue bounds one value a sign-in asks for, as maxImportFile bounds the file the import
// command reads: a service account key is a document of a few kilobytes, and standard input may
// never end.
const maxInputValue = 1 << 20

// documentField says a field takes a JSON file, whose value may span several lines. The protocol
// has no mark for it, so it is the one such input the extensions have: Vertex's service account
// key. Any other value is one line, whatever it starts with.
func documentField(f protocol.LoginInput) bool { return f.Name == "service_account" }

// hiddenField says the terminal's echo stays off while the field is read.
func hiddenField(f protocol.LoginInput) bool { return f.Secret || documentField(f) }

// inputValue gathers one field's value from its lines. A document is scanned once, as it comes,
// for where its outer brace closes, and only then parsed, so a long one costs no more per line
// than a short one.
type inputValue struct {
	document bool
	text     strings.Builder
	open     bool
	depth    int
	inString bool
	escaped  bool
}

func newInputValue(f protocol.LoginInput) *inputValue {
	return &inputValue{document: documentField(f)}
}

// add is one more line of the value: the value and true when it is complete.
func (v *inputValue) add(r lineRead) (string, bool, error) {
	size := len(r.text)
	if v.open {
		size += v.text.Len() + 1
	}
	if r.tooLong || size > maxInputValue {
		return "", false, usageError{fmt.Sprintf("a value is over the %d KiB a sign-in takes", maxInputValue>>10)}
	}
	if !v.open {
		if !v.document || !strings.HasPrefix(strings.TrimSpace(r.text), "{") {
			return r.text, true, nil
		}
		v.open = true
	} else {
		v.text.WriteByte('\n')
	}
	v.text.WriteString(r.text)
	if !v.closes(r.text) {
		return "", false, nil
	}
	s := v.text.String()
	if !json.Valid([]byte(s)) {
		return "", false, usageError{"the value is not a valid JSON document"}
	}
	return s, true, nil
}

// closes reads one line of a document and says whether its outermost brace closed in it.
func (v *inputValue) closes(line string) bool {
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case v.escaped:
			v.escaped = false
		case v.inString:
			if c == '\\' {
				v.escaped = true
			} else if c == '"' {
				v.inString = false
			}
		case c == '"':
			v.inString = true
		case c == '{' || c == '[':
			v.depth++
		case c == '}' || c == ']':
			v.depth--
			if v.depth == 0 {
				return true
			}
		}
	}
	return false
}

// take is one line of standard input for the field that waits for it, and says whether it
// completed that field's value. An empty line answers a field that may be left empty with nothing,
// which is not sent.
func (g *promptInput) take(r lineRead) (bool, error) {
	f := g.next()
	if f == nil {
		return false, nil
	}
	if g.value == nil {
		g.value = newInputValue(*f)
	}
	v, complete, err := g.value.add(r)
	if err != nil || !complete {
		return false, err
	}
	g.value = nil
	g.done[f.Name] = true
	if v != "" || !optionalInput(*f, g.kept) {
		g.got[f.Name] = v
	}
	return true, nil
}

// endOfInput is standard input's end. A prompt that can finish by itself goes on. One whose
// required fields all have a value is left complete, so what it has is sent; one that must have a
// value it did not get cannot go on, and the sign-in is cancelled with a usage error.
func (g *promptInput) endOfInput(x *invocation, login string) error {
	f := g.next()
	if g.kind != protocol.PromptInput {
		g.eof = true
		return nil
	}
	if g.value != nil && f != nil {
		return x.cancelLogin(login, usageError{fmt.Sprintf("%s is not a complete JSON document, and standard input ended before its end", fieldName(*f))})
	}
	for _, f := range g.fields {
		if g.done[f.Name] || optionalInput(f, g.kept) {
			continue
		}
		return x.cancelLogin(login, usageError{fmt.Sprintf("the sign-in needs %s and standard input has no more lines: %s", fieldName(f), inputHint(f))})
	}
	for _, f := range g.fields {
		g.done[f.Name] = true
	}
	return nil
}

// sendInput sends what a prompt asked for. It does not read the sign-in again: an extension
// updates its prompt on its own goroutine, so what comes back at once may still be the prompt it
// just answered, and followLogin reads it again until it changes or ends.
func (x *invocation) sendInput(ctx context.Context, id string, input map[string]string) error {
	_, err := x.client.Call(ctx, http.MethodPost, "/logins/"+url.PathEscape(id)+"/input", nil, management.LoginInputRequest{Input: input}, nil)
	return err
}

// loginRetryable says whether reading or answering a sign-in can succeed on another try: an
// answer core will not give again (404, or a refused key) cannot, and neither can a call whose
// context has ended.
func loginRetryable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var ae *management.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			return false
		}
	}
	return true
}

// readLogin is GET /logins/{id}, whose body is what --json prints at the end.
func (x *invocation) readLogin(ctx context.Context, id string) (management.Login, []byte, error) {
	var reply management.LoginReply
	body, err := x.client.Call(ctx, http.MethodGet, "/logins/"+url.PathEscape(id), nil, nil, &reply)
	if err != nil {
		return management.Login{}, nil, err
	}
	return reply.Login, body, nil
}

// writeBody prints a reply body unchanged, as --json does.
func (x *invocation) writeBody(body []byte) error {
	if _, err := x.stdout.Write(body); err != nil {
		return err
	}
	if !strings.HasSuffix(string(body), "\n") {
		x.printf("\n")
	}
	return nil
}

// loginEnded is what a finished sign-in prints and how the command exits.
func (x *invocation) loginEnded(l management.Login, body []byte, again bool) error {
	if x.json {
		if err := x.writeBody(body); err != nil {
			return err
		}
	}
	switch l.State {
	case management.LoginDone:
		if !x.json {
			if again {
				x.printf("account %s signed in again\n", l.Account)
			} else {
				x.printf("account %s added\n", l.Account)
			}
		}
		return nil
	case management.LoginCancelled:
		return errors.New("the sign-in was cancelled")
	}
	reason := ""
	if l.Error != nil && l.Error.Message != "" {
		reason = l.Error.Message
	}
	if reason == "" {
		return fmt.Errorf("the sign-in failed (%s)", l.State)
	}
	return fmt.Errorf("the sign-in failed: %s", reason)
}

// cancelLogin stops a sign-in the command gives up on and returns err, the reason it did. It says
// when core did not take the cancel, since such a sign-in is still in core.
func (x *invocation) cancelLogin(id string, err error) error {
	// A terminal with a secret's echo still off is put back first: the cancel call below waits up
	// to five seconds for core.
	if x.restoreEcho != nil {
		x.restoreEcho()
	}
	// The command's own context may be the one that ended (Ctrl-C), so the cancel call gets a
	// context of its own; core keeps a sign-in no one cancelled, so it is worth the try.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(x.ctx), 5*time.Second)
	defer cancel()
	if _, cerr := x.client.Call(ctx, http.MethodDelete, "/logins/"+url.PathEscape(id), nil, nil, nil); cerr != nil {
		return fmt.Errorf("%w (the sign-in was not cancelled in core: %v)", err, cerr)
	}
	return err
}

// loginPrompt writes what a sign-in asks the person to do: its message, the address to open or
// the code to type.
func (x *invocation) loginPrompt(p *protocol.Prompt) {
	if p == nil {
		return
	}
	if p.Message != "" {
		if stderrIsTerminal(x.stderr) {
			_ = writeEscaped(x.stderr, []byte(p.Message+"\n"))
		} else {
			x.eprintf("%s\n", p.Message)
		}
	}
	if p.Kind == protocol.PromptDevice && p.UserCode != "" {
		x.eprintf("code: %s\n", x.errCell(p.UserCode))
	}
	if u := promptURL(p); u != "" {
		x.eprintf("open: %s\n", x.errCell(u))
	}
}

// promptKey names one prompt, so the same one shown again is not printed twice.
func promptKey(p *protocol.Prompt) string {
	if p == nil {
		return ""
	}
	b, _ := json.Marshal(p)
	return string(b)
}

func promptURL(p *protocol.Prompt) string {
	if p.Kind == protocol.PromptDevice {
		if p.VerificationURIComplete != "" {
			return p.VerificationURIComplete
		}
		return p.VerificationURI
	}
	return p.URL
}

// startInputs is what the sign-in asks for before it starts, from --input or standard input. A
// required one it did not get is a usage error, and nothing is started.
func (x *invocation) startInputs(fields []protocol.LoginInput, in signInInputs, lines *lineReader) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range fields {
		if v, ok := in.vals[f.Name]; ok {
			if f.Secret {
				return nil, usageError{fmt.Sprintf("%s is secret: the sign-in reads it from a prompt or standard input, never from --input", fieldName(f))}
			}
			out[f.Name] = v
			delete(in.vals, f.Name)
			continue
		}
		if err := x.asks(f); err != nil {
			return nil, err
		}
		// The echo is set before the label is shown, and stays as it is until the value is whole.
		lines.field(&f)
		if x.prompts() {
			x.eprintf("%s: ", x.errCell(fieldName(f)))
		}
		value := newInputValue(f)
		var text string
		for {
			r, err := lines.next(x.ctx)
			if err != nil {
				return nil, err
			}
			if !r.ok {
				if lines.hidden() {
					x.eprintf("\n")
				}
				if value.open {
					return nil, usageError{fmt.Sprintf("%s is not a complete JSON document, and standard input ended before its end", fieldName(f))}
				}
				if optionalInput(f, in.kept) {
					break
				}
				return nil, usageError{fmt.Sprintf("the sign-in needs %s: %s", fieldName(f), inputHint(f))}
			}
			var complete bool
			if text, complete, err = value.add(r); err != nil {
				return nil, err
			}
			if complete {
				if lines.hidden() {
					x.eprintf("\n")
				}
				break
			}
		}
		if text != "" || !optionalInput(f, in.kept) {
			out[f.Name] = text
		}
	}
	// Core may take a while to answer, and nothing is being typed for it.
	lines.show()
	return out, nil
}

// fieldName is how a sign-in's input is named to a person.
func fieldName(f protocol.LoginInput) string {
	if f.Label != "" {
		return f.Label
	}
	return f.Name
}

// inputHint says how to give a value a sign-in wants.
func inputHint(f protocol.LoginInput) string {
	if f.Secret {
		return "write it on standard input, one line for each value the sign-in asks for (it is secret, so no flag takes it)"
	}
	return fmt.Sprintf("give it with --input %s=<value>, or on standard input, one line for each value the sign-in asks for", f.Name)
}

// browserWaitLine says what happens to a browser sign-in once standard input has ended: its input
// is only the fallback for a callback the extension may still receive (next/protocol/README.md).
func browserWaitLine() string {
	return fmt.Sprintf("standard input has ended, so nothing more can be pasted. The sign-in can now finish only if the browser's return reaches its provider; core gives up on it %s after it started. Ctrl-C cancels it.", humanSpan(cred.LoginTimeout))
}

// lineReader reads standard input one line at a time, on a goroutine of its own that takes no
// requests and never touches the terminal, so a sign-in can be read again while a person types.
// The goroutine starts with the first value asked for: a command that needs none leaves standard
// input to whatever reads it next. The terminal's echo is set by the one goroutine that follows the
// sign-in, through field, and close turns it back on.
type lineReader struct {
	src     *bufio.Reader
	term    *os.File
	lines   chan lineRead
	done    chan struct{}
	started sync.Once
	closed  sync.Once
	// restore turns the terminal's echo back on while it is off; nil otherwise.
	restore func()
	// finished is closed once the goroutine has stopped reading: lines may still hold what it read.
	finished chan struct{}
}

// lineRead is one line of standard input, or its end (ok false, as a closed channel gives).
// tooLong is a line over the most a value may be, of which the text is not kept.
type lineRead struct {
	text    string
	ok      bool
	tooLong bool
}

// typedAhead is how many lines may wait to be received, each at most maxInputValue.
const typedAhead = 16

// newLineReader reads r's lines. term is standard input when it is a terminal, whose echo a
// secret value is kept from; nil when there is none.
func newLineReader(r io.Reader, term *os.File) *lineReader {
	return &lineReader{src: bufio.NewReaderSize(r, 64<<10), term: term,
		lines: make(chan lineRead, typedAhead), done: make(chan struct{}), finished: make(chan struct{})}
}

// start begins reading, once.
func (l *lineReader) start() { l.started.Do(func() { go l.read() }) }

// read sends each line, and closes the channel at the end of input or after a line too long.
func (l *lineReader) read() {
	defer close(l.finished)
	defer close(l.lines)
	for {
		text, tooLong, err := readBoundedLine(l.src)
		if err != nil && text == "" && !tooLong {
			return
		}
		select {
		case l.lines <- lineRead{text: text, ok: true, tooLong: tooLong}:
		case <-l.done:
			return
		}
		if tooLong || err != nil {
			return
		}
	}
}

// readBoundedLine is the next line without its ending. It stops at a line over maxInputValue
// rather than holding all of one that may never end.
func readBoundedLine(src *bufio.Reader) (string, bool, error) {
	var line []byte
	for {
		part, err := src.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > maxInputValue+len("\r\n") {
			return "", true, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return strings.TrimRight(string(line), "\r\n"), false, err
	}
}

// field sets the terminal's echo for the field now being read, or for none: off for a value that
// must not show, on otherwise. It changes the terminal only when that differs from what it is, so
// the echo stays off across all the lines of one value.
func (l *lineReader) field(f *protocol.LoginInput) {
	if f == nil || !hiddenField(*f) {
		l.show()
		return
	}
	if l.restore == nil && l.term != nil {
		l.restore = terminalNoEcho(l.term)
	}
}

// show turns the terminal's echo back on.
func (l *lineReader) show() {
	if l.restore != nil {
		restore := l.restore
		l.restore = nil
		restore()
	}
}

// hidden says whether the terminal's echo is off now, so a line ending has to be written.
func (l *lineReader) hidden() bool { return l.restore != nil }

// dropTyped drops the lines already waiting on a terminal: they were typed for the prompt before
// this one. Piped lines are never dropped, since they go to the fields in order.
func (l *lineReader) dropTyped() {
	if l.term == nil {
		return
	}
	for {
		select {
		case _, open := <-l.lines:
			if !open {
				return
			}
		default:
			return
		}
	}
}

func (l *lineReader) close() {
	l.closed.Do(func() {
		close(l.done)
		l.show()
	})
}

// next is the next line; ok is false when standard input has ended.
func (l *lineReader) next(ctx context.Context) (lineRead, error) {
	l.start()
	select {
	case <-ctx.Done():
		return lineRead{}, ctx.Err()
	case r := <-l.lines:
		return r, nil
	}
}

// runAccountRemove is pluxy account remove <id>. It does not ask, as the command's other
// removes do not; the ID names the one account it takes.
func runAccountRemove(x *invocation) error {
	return x.call(http.MethodDelete, accountPath(x.args[0]), nil, nil, nil, func() {
		x.printf("account %s removed\n", x.args[0])
	})
}

// runAccountRefresh is pluxy account refresh <id>: it renews the account's credential now.
func runAccountRefresh(x *invocation) error {
	var r management.AccountReply
	return x.call(http.MethodPost, accountPath(x.args[0])+"/refresh", nil, nil, &r, func() {
		line := fmt.Sprintf("account %s refreshed", r.Account.ID)
		if r.Account.ExpiresAt > 0 {
			line += ": its credential expires " + time.Unix(r.Account.ExpiresAt, 0).Local().Format(time.DateTime)
		}
		x.printf("%s\n", line)
	})
}

// accountSetJSON are the fields PATCH /accounts/{id} takes, by their JSON names: true for a
// field whose value is a JSON document, false for one whose value is a plain string.
var accountSetJSON = map[string]bool{
	"disabled":        true,
	"priority":        true,
	"weight":          true,
	"disable_cooling": true,
	"max_concurrency": true,
	"request_retry":   true,
	"prefix":          false,
	"models":          true,
	"excluded_models": true,
	"model_alias":     true,
	"proxy_url":       false,
	"headers":         true,
	"timeouts":        true,
	"settings":        true,
}

// runAccountSet is pluxy account set <id> name=value...: the account's routing settings, and
// disabled, changed as PATCH /accounts/{id} changes them.
func runAccountSet(x *invocation) error {
	body := map[string]json.RawMessage{}
	for _, pair := range x.args[1:] {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name == "" {
			return usageError{fmt.Sprintf("%q is not name=value", pair)}
		}
		jsonValue, known := accountSetJSON[name]
		if !known {
			return usageError{fmt.Sprintf("%q cannot be changed here: send any of %s", name, strings.Join(slices.Sorted(maps.Keys(accountSetJSON)), ", "))}
		}
		raw, err := setValue(name, value, jsonValue)
		if err != nil {
			return err
		}
		body[name] = raw
	}
	var r management.AccountReply
	return x.call(http.MethodPatch, accountPath(x.args[0]), nil, body, &r, func() {
		x.printf("account %s changed\n", r.Account.ID)
	})
}

// setValue is the JSON one name=value on the command line sends: null sets the field back to
// its default (core refuses it for disabled, which is only true or false), a plain-string field
// takes the value as it is, and the rest must be JSON.
func setValue(name, value string, jsonValue bool) (json.RawMessage, error) {
	if value == "null" {
		return json.RawMessage("null"), nil
	}
	if !jsonValue {
		b, err := json.Marshal(value)
		return b, err
	}
	if !json.Valid([]byte(value)) {
		return nil, usageError{fmt.Sprintf("%s takes a JSON value, such as 5, true, [\"m\"], {...} or null", name)}
	}
	return json.RawMessage(value), nil
}

// maxImportFile bounds the file pluxy account import reads: core takes 1 MiB (POST
// /accounts/import reads a file of up to 1 MiB, and POST /vertex/import 1 MiB in all).
const maxImportFile = 1 << 20

// runAccountImport is pluxy account import <file>. The file says which call takes it: a Google
// service account key (a JSON object holding a private_key and a project_id) goes to POST
// /vertex/import, and every other credential file to POST /accounts/import, whose name gives the
// account's ID. A file named - is a service account key read from standard input.
func runAccountImport(x *invocation) error {
	path := x.args[0]
	var raw []byte
	var name string
	var err error
	if path == "-" {
		raw, err = io.ReadAll(io.LimitReader(x.stdin, maxImportFile+1))
	} else {
		// What is read is a credential file, not a stream: a device or a pipe never ends, and the
		// 1 MiB core takes is refused before a byte of it is read.
		var st os.FileInfo
		if st, err = os.Stat(path); err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return usageError{fmt.Sprintf("%s is not a regular file: give it a credential file", path)}
		}
		if st.Size() > maxImportFile {
			return fmt.Errorf("%s is larger than the 1 MiB core reads", path)
		}
		var f *os.File
		if f, err = os.Open(path); err != nil {
			return err
		}
		defer f.Close()
		raw, err = io.ReadAll(io.LimitReader(f, maxImportFile+1))
		name = filepath.Base(path)
	}
	if err != nil {
		return err
	}
	if len(raw) > maxImportFile {
		return fmt.Errorf("%s is larger than the 1 MiB core reads", dash(path))
	}
	if serviceAccountKey(raw) {
		form, err := management.NewMultipartBody("file", "service-account.json", raw, nil)
		if err != nil {
			return err
		}
		var r management.VertexImportReply
		body, err := x.client.CallForm(x.ctx, http.MethodPost, "/vertex/import", nil, form, &r)
		if err != nil {
			return err
		}
		if x.json {
			return x.writeBody(body)
		}
		line := fmt.Sprintf("account %s imported: project %s, location %s", x.cell(r.Account), x.cell(r.ProjectID), x.cell(r.Location))
		if r.Email != "" {
			line += ", " + x.cell(r.Email)
		}
		x.printf("%s\n", line)
		return nil
	}
	if path == "-" {
		return usageError{"standard input holds no service account key (a JSON object with a private_key and a project_id); a credential file read from standard input has no name, and its name gives the account's ID"}
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") || len(name) <= len(".json") {
		return usageError{fmt.Sprintf("%s is not a .json credential file: its name gives the account's ID", path)}
	}
	var r management.AuthImportReply
	return x.call(http.MethodPost, "/accounts/import", nil, management.AuthImportRequest{Name: name, Content: string(raw)}, &r, func() {
		if len(r.Imported) == 0 && len(r.Skipped) == 0 {
			x.printf("no account imported\n")
		}
		if len(r.Imported) > 0 {
			rows := [][]string{{"ACCOUNT", "EXTENSION", "LEFT OUT"}}
			for _, a := range r.Imported {
				left := "-"
				if len(a.Ignored) > 0 {
					left = strings.Join(a.Ignored, ", ")
				}
				rows = append(rows, []string{a.ID, dash(a.Extension), left})
			}
			x.table(rows)
		}
		for _, why := range r.Skipped {
			x.printf("skipped: %s\n", x.cell(why))
		}
	})
}

// serviceAccountKey tells a Google service account key from today's other credential files: a
// JSON object holding the private key and the project the Vertex import signs in to.
func serviceAccountKey(raw []byte) bool {
	var key struct {
		PrivateKey string `json:"private_key"`
		ProjectID  string `json:"project_id"`
	}
	if json.Unmarshal(raw, &key) != nil {
		return false
	}
	return strings.TrimSpace(key.PrivateKey) != "" && strings.TrimSpace(key.ProjectID) != ""
}

// runAccountQuota is pluxy account quota <id>: the account's plan and usage windows, written as
// the panel writes them (percents rounded to two places, each reset with the wait it has left);
// --json stays the reply unchanged.
func runAccountQuota(x *invocation) error {
	var r management.QuotaReply
	return x.call(http.MethodGet, accountPath(x.args[0])+"/quota", nil, nil, &r, func() { printQuota(x, r) })
}

func printQuota(x *invocation, r management.QuotaReply) {
	q := r.Quota
	if q == nil {
		x.printf("no quota reported\n")
		return
	}
	if q.Plan != "" {
		x.printf("plan: %s\n", x.cell(q.Plan))
	}
	if q.PlanEndsAt > 0 {
		x.printf("subscription ends: %s\n", time.Unix(q.PlanEndsAt, 0).Local().Format("2006-01-02 15:04"))
	}
	if !r.FetchedAt.IsZero() {
		x.printf("fetched: %s\n", r.FetchedAt.Local().Format(time.DateTime))
	}
	var limits []protocol.QuotaLimit
	manual := ""
	for _, l := range q.Limits {
		if l.ID == "reset_credits" {
			manual = "-"
			if l.Remaining != nil {
				manual = comma(*l.Remaining)
			}
			continue
		}
		limits = append(limits, l)
	}
	if manual != "" {
		x.printf("manual resets: %s\n", manual)
	}
	if len(limits) == 0 {
		x.printf("no usage limits reported\n")
		return
	}
	rows := [][]string{{"LIMIT", "USED", "PERCENT", "RESETS", "WINDOW"}}
	for _, l := range limits {
		rows = append(rows, []string{quotaLabel(l), quotaAmount(l), quotaPercent(l), quotaReset(l), quotaWindow(l)})
	}
	x.printf("\n")
	x.table(rows)
}

func quotaLabel(l protocol.QuotaLimit) string {
	if l.Label != "" {
		return l.Label
	}
	return dash(l.ID)
}

// quotaPercent is the share of the limit used, as the panel shows it: what the provider says,
// else what the amounts say, rounded to two places.
func quotaPercent(l protocol.QuotaLimit) string {
	p := l.UsedPercent
	if p == nil && l.Limit != nil && *l.Limit > 0 && (l.Used != nil || l.Remaining != nil) {
		used := *l.Limit
		if l.Used != nil {
			used = *l.Used
		} else {
			used -= *l.Remaining
		}
		pct := used / *l.Limit * 100
		p = &pct
	}
	if p == nil {
		return "-"
	}
	return strconv.FormatFloat(math.Round(*p*100)/100, 'f', -1, 64) + "%"
}

// quotaAmount is what the limit counts: unlimited, the amounts with their unit, and how much is
// left.
func quotaAmount(l protocol.QuotaLimit) string {
	var parts []string
	unit := ""
	if l.Unit != "" {
		unit = " " + l.Unit
	}
	switch {
	case l.Unlimited:
		parts = append(parts, "unlimited")
	case l.Used != nil && l.Limit != nil:
		parts = append(parts, comma(*l.Used)+" / "+comma(*l.Limit)+unit)
	case l.Used != nil:
		parts = append(parts, comma(*l.Used)+unit+" used")
	case l.Limit != nil:
		parts = append(parts, "limit "+comma(*l.Limit)+unit)
	}
	if l.Remaining != nil {
		parts = append(parts, comma(*l.Remaining)+unit+" remaining")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}

// quotaReset is when the limit resets, with the wait left, as the panel shows it.
func quotaReset(l protocol.QuotaLimit) string {
	if l.ResetsAt == 0 {
		return "-"
	}
	at := time.Unix(l.ResetsAt, 0).Local()
	return at.Format("2006-01-02 15:04") + " (in " + humanSpan(time.Until(at)) + ")"
}

// quotaWindow is the length of the limit's usage window.
func quotaWindow(l protocol.QuotaLimit) string {
	if l.WindowSeconds <= 0 {
		return "-"
	}
	return humanSpan(time.Duration(l.WindowSeconds) * time.Second)
}

// humanSpan writes a length of time as the panel's quota page does: whole days, hours, minutes
// or seconds, rounded up.
func humanSpan(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d >= 24*time.Hour:
		return plural(math.Ceil(d.Hours()/24), "day")
	case d >= time.Hour:
		return plural(math.Ceil(d.Hours()), "hour")
	case d >= time.Minute:
		return plural(math.Ceil(d.Minutes()), "minute")
	}
	return plural(math.Ceil(d.Seconds()), "second")
}

func plural(n float64, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.FormatFloat(n, 'f', -1, 64) + " " + unit + "s"
}

// comma writes an amount with thousands separators, as the panel does.
func comma(v float64) string {
	if v == math.Trunc(v) {
		return groupThousands(strconv.FormatFloat(v, 'f', 0, 64))
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	whole, frac, _ := strings.Cut(s, ".")
	return groupThousands(whole) + "." + frac
}

// groupThousands puts commas between the digits of whole, a plain integer.
func groupThousands(whole string) string {
	sign := ""
	if strings.HasPrefix(whole, "-") {
		sign, whole = "-", whole[1:]
	}
	var b strings.Builder
	for i := range len(whole) {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(whole[i])
	}
	return sign + b.String()
}
