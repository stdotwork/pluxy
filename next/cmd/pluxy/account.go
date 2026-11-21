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
// seconds too.
const accountLoginWait = 2 * time.Second

// accountLoginTimeout bounds a sign-in the command follows, so a provider that never ends one
// does not hold the terminal for ever. A sign-in's own prompt expires long before.
const accountLoginTimeout = 30 * time.Minute

// waitingError ends a command that started something a person must finish elsewhere, such as a
// sign-in that needs a browser: exit 7 (next/management/README.md).
type waitingError struct{ msg string }

func (e waitingError) Error() string { return e.msg }

// inputFlag collects the --input name=value pairs: what the person entered for the inputs a
// sign-in asks for. A secret is refused here (a flag stands in the shell's history and in the
// process list) and read from a prompt or standard input instead.
type inputFlag struct{ values map[string]string }

func (f *inputFlag) String() string {
	var pairs []string
	for name, value := range f.values {
		pairs = append(pairs, name+"="+value)
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

func (f *inputFlag) Set(v string) error {
	name, value, ok := strings.Cut(v, "=")
	if !ok || name == "" {
		return errors.New("write it as name=value")
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

// pickMethod is the method named by want, or the only one the extension offers.
func pickMethod(m *management.LoginMethods, want string) (protocol.LoginMethod, error) {
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
	return protocol.LoginMethod{}, usageError{fmt.Sprintf("%s signs in %d ways; name one with --method: %s", m.Extension, len(m.Methods), strings.Join(labels, ", "))}
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
	method, err := pickMethod(ext, x.opts.method)
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
	vals := maps.Clone(x.opts.input.values)
	input, err := x.startInputs(method.Inputs, vals, lines)
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
	return x.afterLoginStart(reply.Login, body, vals, lines, false)
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
	method, err := pickMethod(offered, x.opts.method)
	if err != nil {
		return err
	}
	lines := newLineReader(x.stdin, x.terminal())
	defer lines.close()
	vals := maps.Clone(x.opts.input.values)
	input, err := x.startInputs(method.Inputs, vals, lines)
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
	return x.afterLoginStart(reply.Login, body, vals, lines, true)
}

// afterLoginStart ends the sign-in a command started: it says where to go and stops (--no-wait,
// exit 7) or follows it to its end. A sign-in that ended at once has nothing to wait for.
func (x *invocation) afterLoginStart(l management.Login, body []byte, vals map[string]string, lines *lineReader, again bool) error {
	if x.opts.noWait && l.State == management.LoginPending {
		x.loginPrompt(l.Prompt)
		if x.json {
			if err := x.writeBody(body); err != nil {
				return err
			}
		}
		return waitingError{fmt.Sprintf("waiting for the sign-in for %s to finish: it goes on in core, and the account appears in pluxy account list when it does", l.Account)}
	}
	return x.followLogin(l, body, vals, lines, again)
}

// followLogin reads a sign-in again until it ends, asking for what its prompts want.
func (x *invocation) followLogin(l management.Login, body []byte, vals map[string]string, lines *lineReader, again bool) error {
	ctx, stop := context.WithTimeout(x.ctx, accountLoginTimeout)
	defer stop()

	shown := ""
	var prompt *promptInput
	input := lines.lines
	awaiting := false
	ticker := time.NewTicker(accountLoginWait)
	defer ticker.Stop()
	for {
		if l.State != management.LoginPending {
			return x.loginEnded(l, body, again)
		}
		if key := promptKey(l.Prompt); l.Prompt != nil && key != shown {
			shown = key
			x.loginPrompt(l.Prompt)
			var err error
			if prompt, err = x.newPromptInput(l.Prompt, vals); err != nil {
				return x.cancelLogin(l.ID, err)
			}
		}
		if prompt != nil && prompt.next() == nil {
			// The prompt wants nothing more: send what a flag or standard input gave, or, with
			// nothing to send, wait below for the sign-in to finish by itself. A prompt that is
			// shown again unchanged is not handled again: nothing more is owed it.
			answered := prompt
			prompt = nil
			if len(answered.got) > 0 {
				shown = ""
				next, nextBody, err := x.answerPrompt(ctx, l.ID, answered.got)
				if err != nil {
					return err
				}
				l, body = next, nextBody
				continue
			}
		}
		if prompt != nil && prompt.next() != nil {
			if x.prompts() {
				prompt.ask(x)
			}
			if !awaiting {
				if err := lines.askLine(ctx, prompt.next().Secret); err != nil {
					return x.cancelLogin(l.ID, err)
				}
				awaiting = true
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return x.cancelLogin(l.ID, fmt.Errorf("the sign-in did not finish in %s; core's login was cancelled", accountLoginTimeout))
			}
			return x.cancelLogin(l.ID, errors.New("sign-in cancelled"))
		case r := <-input:
			awaiting = false
			if !r.ok {
				input = nil
				if prompt != nil {
					if err := prompt.endOfInput(x, l.ID); err != nil {
						return err
					}
				}
				continue
			}
			if r.hidden {
				x.eprintf("\n")
			}
			if prompt != nil {
				prompt.take(r.text)
			}
		case <-ticker.C:
			next, nextBody, err := x.readLogin(ctx, l.ID)
			if err != nil {
				return err
			}
			l, body = next, nextBody
		}
	}
}

// promptInput gathers what one prompt of a sign-in asks for.
type promptInput struct {
	kind   string
	fields []protocol.LoginInput
	got    map[string]string
	asked  map[string]bool
	eof    bool
}

// newPromptInput is the gathering for p: each value a flag gave is taken, except a secret's,
// which no flag may give.
func (x *invocation) newPromptInput(p *protocol.Prompt, vals map[string]string) (*promptInput, error) {
	g := &promptInput{kind: p.Kind, fields: p.Inputs, got: map[string]string{}, asked: map[string]bool{}}
	for _, f := range p.Inputs {
		v, ok := vals[f.Name]
		if !ok {
			continue
		}
		if f.Secret {
			return nil, usageError{fmt.Sprintf("%s is secret: the sign-in reads it from a prompt or standard input, never from --input", fieldName(f))}
		}
		g.got[f.Name] = v
		delete(vals, f.Name)
	}
	return g, nil
}

// next is the field whose value the sign-in still waits for, or nil when it waits for no more.
func (g *promptInput) next() *protocol.LoginInput {
	if g.eof {
		return nil
	}
	for i := range g.fields {
		if _, ok := g.got[g.fields[i].Name]; !ok {
			return &g.fields[i]
		}
	}
	return nil
}

// ask writes the label of the field standard input is about to read, once for each field.
func (g *promptInput) ask(x *invocation) {
	f := g.next()
	if f == nil || g.asked[f.Name] {
		return
	}
	g.asked[f.Name] = true
	x.eprintf("%s: ", fieldName(*f))
}

// take is one line of standard input: the value of the field that waits for it. An empty line
// leaves an optional field out.
func (g *promptInput) take(line string) {
	f := g.next()
	if f == nil {
		return
	}
	if line != "" || !f.Optional {
		g.got[f.Name] = line
	}
}

// endOfInput is standard input's end: a sign-in that must have an input it did not get cannot go
// on, so it is cancelled with a usage error; one that can finish by itself goes on.
func (g *promptInput) endOfInput(x *invocation, login string) error {
	g.eof = true
	if g.kind != protocol.PromptInput {
		return nil
	}
	for _, f := range g.fields {
		if _, ok := g.got[f.Name]; ok || f.Optional {
			continue
		}
		return x.cancelLogin(login, usageError{fmt.Sprintf("the sign-in needs %s and standard input has no more lines: %s", fieldName(f), inputHint(f))})
	}
	return nil
}

// answerPrompt sends what the prompt asked for and reads the sign-in again, so a prompt that
// comes back unchanged is not answered twice with one input.
func (x *invocation) answerPrompt(ctx context.Context, id string, input map[string]string) (management.Login, []byte, error) {
	if _, err := x.client.Call(ctx, http.MethodPost, "/logins/"+url.PathEscape(id)+"/input", nil, management.LoginInputRequest{Input: input}, nil); err != nil {
		return management.Login{}, nil, err
	}
	return x.readLogin(ctx, id)
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

// cancelLogin stops a sign-in the command gives up on and returns err, the reason it did.
func (x *invocation) cancelLogin(id string, err error) error {
	// The command's own context may be the one that ended (Ctrl-C), so the cancel call gets a
	// context of its own; core keeps a sign-in no one cancelled, so it is worth the try.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(x.ctx), 5*time.Second)
	defer cancel()
	_, _ = x.client.Call(ctx, http.MethodDelete, "/logins/"+url.PathEscape(id), nil, nil, nil)
	return err
}

// loginPrompt writes what a sign-in asks the person to do: its message, the address to open or
// the code to type.
func (x *invocation) loginPrompt(p *protocol.Prompt) {
	if p == nil {
		return
	}
	if p.Message != "" {
		x.eprintf("%s\n", p.Message)
	}
	if p.Kind == protocol.PromptDevice && p.UserCode != "" {
		x.eprintf("code: %s\n", p.UserCode)
	}
	if u := promptURL(p); u != "" {
		x.eprintf("open: %s\n", u)
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
func (x *invocation) startInputs(fields []protocol.LoginInput, vals map[string]string, lines *lineReader) (map[string]string, error) {
	out := map[string]string{}
	for _, f := range fields {
		if v, ok := vals[f.Name]; ok {
			if f.Secret {
				return nil, usageError{fmt.Sprintf("%s is secret: the sign-in reads it from a prompt or standard input, never from --input", fieldName(f))}
			}
			out[f.Name] = v
			delete(vals, f.Name)
			continue
		}
		if x.prompts() {
			x.eprintf("%s: ", fieldName(f))
		}
		r, err := lines.readline(x.ctx, f.Secret)
		if err != nil {
			return nil, err
		}
		if r.hidden {
			x.eprintf("\n")
		}
		if !r.ok && !f.Optional {
			return nil, usageError{fmt.Sprintf("the sign-in needs %s: %s", fieldName(f), inputHint(f))}
		}
		if r.ok && (r.text != "" || !f.Optional) {
			out[f.Name] = r.text
		}
	}
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

// lineReader reads standard input one line at a time, on a goroutine of its own, so a sign-in can
// be read again while a person types. The command asks for each line, so it can name a secret one
// before it is typed: a terminal then keeps it off the screen. close ends the reader and turns the
// terminal's echo back on.
type lineReader struct {
	src   *bufio.Reader
	term  *os.File
	ask   chan bool
	lines chan lineRead
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	// restore turns the terminal's echo back on while a secret line is read; nil otherwise.
	restore func()
}

// lineRead is one line of standard input, or its end (ok false); hidden says the terminal's echo
// was off while it was typed, so a line ending has to be written.
type lineRead struct {
	text   string
	ok     bool
	hidden bool
}

// newLineReader reads r's lines. term is standard input when it is a terminal, whose echo a
// secret line is kept from; nil when there is none.
func newLineReader(r io.Reader, term *os.File) *lineReader {
	l := &lineReader{src: bufio.NewReaderSize(r, 64<<10), term: term, ask: make(chan bool), lines: make(chan lineRead), done: make(chan struct{})}
	go func() {
		defer close(l.lines)
		for {
			var secret bool
			select {
			case secret = <-l.ask:
			case <-l.done:
				return
			}
			hidden := l.hide(secret)
			line, err := l.src.ReadString('\n')
			l.show()
			if err != nil && line == "" {
				select {
				case l.lines <- lineRead{}:
				case <-l.done:
				}
				return
			}
			select {
			case l.lines <- lineRead{text: strings.TrimRight(line, "\r\n"), ok: true, hidden: hidden}:
			case <-l.done:
				return
			}
		}
	}()
	return l
}

// hide takes a secret line off the terminal, if secret and there is one, and says whether it did.
func (l *lineReader) hide(secret bool) bool {
	if !secret || l.term == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.restore = terminalNoEcho(l.term)
	return l.restore != nil
}

// show turns the terminal's echo back on after a line.
func (l *lineReader) show() {
	l.mu.Lock()
	restore := l.restore
	l.restore = nil
	l.mu.Unlock()
	if restore != nil {
		restore()
	}
}

func (l *lineReader) close() {
	l.once.Do(func() {
		close(l.done)
		l.show()
	})
}

// askLine asks for the next line, naming it a secret one to keep off the terminal.
func (l *lineReader) askLine(ctx context.Context, secret bool) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case l.ask <- secret:
		return nil
	}
}

// readline is the next line; ok is false when standard input has ended.
func (l *lineReader) readline(ctx context.Context, secret bool) (lineRead, error) {
	if err := l.askLine(ctx, secret); err != nil {
		return lineRead{}, err
	}
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
// its default, a plain-string field takes the value as it is, and the rest must be JSON.
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
		raw, err = os.ReadFile(path)
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
		line := fmt.Sprintf("account %s imported: project %s, location %s", r.Account, r.ProjectID, r.Location)
		if r.Email != "" {
			line += ", " + r.Email
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
			x.printf("skipped: %s\n", why)
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
		x.printf("plan: %s\n", q.Plan)
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
