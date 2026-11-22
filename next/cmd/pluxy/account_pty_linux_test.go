//go:build linux

package main

// A sign-in on a real terminal. The terminal is a pty the test opens itself (/dev/ptmx, with the
// standard library's syscall package), so the echo the kernel gives is what the test reads: not
// what the command believes it did. The command runs in a child process of this test binary, so a
// signal is a real one and the terminal outlives it.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// What the child reads from its environment: which run to make, and the value the terminal is
// given. The value is made in the parent and kept out of every file.
const (
	ptyChildEnv  = "PLUXY_TEST_PTY_SCENARIO"
	ptySecretEnv = "PLUXY_TEST_PTY_SECRET"
	ptyMarker    = "pluxy-pty:"
)

// The runs.
const (
	ptySecret        = "secret"         // a secret field, typed and answered
	ptySigint        = "sigint"         // Ctrl-C while the secret is being typed
	ptySigterm       = "sigterm"        // SIGTERM at the same point
	ptyHangup        = "hangup"         // SIGHUP at the same point
	ptySwitch        = "switch"         // the prompt changes to a secret field while a plain one is read
	ptyJSON          = "json"           // --json on a terminal, which asks nothing and reads nothing
	ptySlowCancel    = "slowcancel"     // SIGTERM while core does not answer the cancel at all
	ptyPaste         = "paste"          // a multi-line JSON key pasted into its field
	ptyBack          = "back"           // a secret field, then a plain one
	ptyStartTerm     = "startterm"      // SIGTERM while a value is read before the sign-in starts
	ptyStartOptEOF   = "start_opt_eof"  // Ctrl-D at an optional start field before a secret start field
	ptyAddress       = "address"        // a browser sign-in: a pasted non-address, then what comes next
	ptyAddressSecret = "address_secret" // a browser sign-in: Ctrl-D, then a secret input prompt
)

// TestCLIAccountTerminal is the test in the parent, and the command's run in the child it starts.
func TestCLIAccountTerminal(t *testing.T) {
	if scenario := os.Getenv(ptyChildEnv); scenario != "" {
		ptyChild(t, scenario)
		return
	}
	// What the terminal showed of a secret: nothing, and the echo is back on when the command
	// ends.
	t.Run("secret", func(t *testing.T) {
		p := startPTY(t, ptySecret)
		p.waitEcho(false)
		p.write(t, p.secret+"\n")
		p.waitExit(t, 0)
		p.waitEcho(true)
		p.wantSecretKept(t)
		p.wantChild(t, "code=0", "cancelled=0", "input=api_key:answer")
	})
	// The echo comes back when the command is interrupted, and the sign-in is cancelled in core.
	t.Run("sigint", func(t *testing.T) {
		p := startPTY(t, ptySigint)
		p.waitEcho(false)
		p.signal(t, syscall.SIGINT)
		p.waitExit(t, 1)
		p.waitEcho(true)
		p.wantChild(t, "code=1", "cancelled=1")
		if !strings.Contains(p.childOutput(), "sign-in cancelled") {
			t.Errorf("Ctrl-C said nothing about the sign-in: %q", p.childOutput())
		}
	})
	t.Run("sigterm", func(t *testing.T) {
		p := startPTY(t, ptySigterm)
		p.waitEcho(false)
		p.signal(t, syscall.SIGTERM)
		p.waitExit(t, 1)
		p.waitEcho(true)
		p.wantChild(t, "code=1", "cancelled=1")
		if !strings.Contains(p.childOutput(), "sign-in cancelled") {
			t.Errorf("SIGTERM said nothing about the sign-in: %q", p.childOutput())
		}
	})
	t.Run("sighup", func(t *testing.T) {
		p := startPTY(t, ptyHangup)
		p.waitEcho(false)
		p.signal(t, syscall.SIGHUP)
		p.waitExit(t, 1)
		p.waitEcho(true)
		p.wantChild(t, "code=1", "cancelled=1")
	})
	// The echo comes back before core is asked to cancel the sign-in: a core that does not answer
	// must not keep the terminal hidden until the cancel call gives up.
	t.Run("slowcancel", func(t *testing.T) {
		p := startPTY(t, ptySlowCancel)
		p.waitEcho(false)
		signalAt := time.Now()
		p.signal(t, syscall.SIGTERM)
		p.waitEcho(true)
		if took := time.Since(signalAt); took > time.Second {
			t.Fatalf("the echo came back after %s: core was asked to cancel before the terminal was put back", took)
		}
		// Core never answers the cancel: the command gives up on it and says so.
		p.waitExit(t, 1)
		p.wantChild(t, "code=1", "cancelled=0")
		if !strings.Contains(p.childOutput(), "not cancelled") {
			t.Errorf("the cancel core never answered was not said: %q", p.childOutput())
		}
	})
	// A prompt that changes to a secret field while the plain one is still being read: the echo
	// goes off for the secret at once, and the line read for the old field is dropped.
	t.Run("switch", func(t *testing.T) {
		p := startPTY(t, ptySwitch)
		p.waitEcho(true)  // the address field, which is not secret
		p.waitEcho(false) // the key field, once core asks for it
		p.waitOutput("API key: ")
		p.write(t, p.secret+"\n")
		p.waitExit(t, 0)
		p.waitEcho(true)
		p.wantSecretKept(t)
		p.wantChild(t, "code=0", "cancelled=0", "inputs=1", "input=api_key:answer")
	})
	// A key pasted in small pieces, thirteen lines of it, shows on the terminal at no point: the
	// echo stays off from its first line to its last.
	t.Run("paste", func(t *testing.T) {
		for run := range 25 {
			p := startPTY(t, ptyPaste)
			p.waitEcho(false)
			p.waitOutput("Service account key: ")
			for typed := p.secret + "\n"; typed != ""; {
				n := min(64, len(typed))
				p.write(t, typed[:n])
				typed = typed[n:]
			}
			p.waitExit(t, 0)
			p.waitEcho(true)
			for _, line := range strings.Split(p.secret, "\n") {
				if value := strings.Trim(line, " {},"); value != "" && strings.Contains(p.echoedText(), value) {
					t.Fatalf("run %d: the terminal showed a line of the key: %q", run, value)
				}
			}
			p.wantChild(t, "code=0", "cancelled=0", "start=answer")
		}
	})
	// Moving from a secret field to a plain one turns the echo back on, so what is typed there
	// shows.
	t.Run("back", func(t *testing.T) {
		p := startPTY(t, ptyBack)
		p.waitEcho(false)
		p.waitOutput("API key: ")
		p.write(t, p.secret+"\n")
		p.waitOutput("Note: ")
		p.waitEcho(true)
		p.write(t, "plain-note\n")
		p.waitExit(t, 0)
		p.wantSecretKept(t)
		if !strings.Contains(p.echoedText(), "plain-note") {
			t.Errorf("the plain field was not echoed: %q", p.echoedText())
		}
		p.wantChild(t, "code=0", "cancelled=0", "inputs=2")
	})
	// A signal while a value is read before the sign-in starts: no sign-in to cancel, so only the
	// reader's close puts the echo back.
	t.Run("startterm", func(t *testing.T) {
		p := startPTY(t, ptyStartTerm)
		p.waitEcho(false)
		p.signal(t, syscall.SIGTERM)
		p.waitExit(t, 1)
		p.waitEcho(true)
		p.wantChild(t, "code=1", "cancelled=0")
	})
	// Ctrl-D at an optional start field must stop asking: a later secret start field prints no
	// label, never turns echo off, starts no sign-in and exits 2.
	t.Run("start Ctrl-D then secret", func(t *testing.T) {
		p := startPTY(t, ptyStartOptEOF)
		p.waitOutput("Note: ")
		p.waitEcho(true)
		p.write(t, "\x04")
		echoOff := false
		deadline := time.Now().Add(20 * time.Second)
		for done := false; !done; {
			if !ptyEcho(t, p.master) {
				echoOff = true
			}
			select {
			case err := <-p.done:
				var exited *exec.ExitError
				got := 0
				switch {
				case errors.As(err, &exited):
					got = exited.ExitCode()
				case err != nil:
					t.Fatalf("the command: %v", err)
				}
				if got != 2 {
					t.Fatalf("the command exited %d, want 2; it wrote %q", got, p.childOutput())
				}
				done = true
			default:
				if time.Now().After(deadline) {
					t.Fatalf("the command did not exit; it wrote %q", p.childOutput())
				}
				time.Sleep(100 * time.Microsecond)
			}
		}
		p.waitEcho(true)
		if echoOff {
			t.Errorf("the terminal's echo was turned off for a start field after standard input ended")
		}
		p.wantChild(t, "code=2", "cancelled=0", "inputs=0", "starts=0", "echo=on")
		out := p.childOutput()
		before, _, _ := strings.Cut(out, "pluxy: ")
		if strings.Contains(before, "API key: ") {
			t.Errorf("the secret field's label was printed after standard input ended: %q", out)
		}
		if !strings.Contains(out, "the sign-in needs API key:") {
			t.Errorf("the end-of-input error was not printed: %q", out)
		}
	})
	// A browser sign-in that was pasted a non-address asks again. The person can paste the right
	// address, press Ctrl-D (the end of input), or press Ctrl-C.
	t.Run("address", func(t *testing.T) {
		pasteWrongScenario := func(t *testing.T, scenario string) *ptySession {
			p := startPTY(t, scenario)
			p.waitOutput("Address the browser ended on: ")
			p.write(t, "not an address\n")
			p.waitOutput("That address holds no sign-in code.")
			return p
		}
		pasteWrong := func(t *testing.T) *ptySession {
			return pasteWrongScenario(t, ptyAddress)
		}
		t.Run("tries again", func(t *testing.T) {
			p := pasteWrong(t)
			p.write(t, "https://localhost/callback?code=ok\n")
			p.waitExit(t, 0)
			p.wantChild(t, "code=0", "cancelled=0", "inputs=2")
		})
		// Ctrl-D ends what can be pasted, not the sign-in: the notice starts on its own line, and
		// Ctrl-C still cancels what goes on waiting.
		t.Run("Ctrl-D", func(t *testing.T) {
			p := pasteWrong(t)
			p.waitOutput("open: https://login.example.test/authorize\nAddress the browser ended on: ")
			p.write(t, "\x04")
			p.waitOutput("Address the browser ended on: \n" + browserWaitNotice)
			p.signal(t, syscall.SIGINT)
			p.waitExit(t, 1)
			p.waitEcho(true)
			p.wantChild(t, "code=1", "cancelled=1", "inputs=1")
			if n := strings.Count(p.childOutput(), browserWaitNotice); n != 1 {
				t.Errorf("the notice was written %d times, want once: %q", n, p.childOutput())
			}
		})
		// After Ctrl-D, a later secret input prompt must not ask for a value or turn echo off:
		// standard input has ended, so the sign-in cancels with exit 2.
		t.Run("Ctrl-D then secret", func(t *testing.T) {
			p := pasteWrongScenario(t, ptyAddressSecret)
			p.waitOutput("open: https://login.example.test/authorize\nAddress the browser ended on: ")
			p.waitEcho(true)
			p.write(t, "\x04")
			echoOff := false
			deadline := time.Now().Add(20 * time.Second)
			for done := false; !done; {
				if !ptyEcho(t, p.master) {
					echoOff = true
				}
				select {
				case err := <-p.done:
					var exited *exec.ExitError
					got := 0
					switch {
					case errors.As(err, &exited):
						got = exited.ExitCode()
					case err != nil:
						t.Fatalf("the command: %v", err)
					}
					if got != 2 {
						t.Fatalf("the command exited %d, want 2; it wrote %q", got, p.childOutput())
					}
					done = true
				default:
					if time.Now().After(deadline) {
						t.Fatalf("the command did not exit; it wrote %q", p.childOutput())
					}
					time.Sleep(100 * time.Microsecond)
				}
			}
			p.waitEcho(true)
			if echoOff {
				t.Errorf("the terminal's echo was turned off for a prompt after standard input ended")
			}
			p.wantChild(t, "code=2", "cancelled=1", "inputs=1", "echo=on")
			out := p.childOutput()
			if strings.Contains(out, "API key: ") {
				t.Errorf("the secret field's label was printed after standard input ended: %q", out)
			}
			if !strings.Contains(out, "Enter the key.") {
				t.Errorf("the prompt's message was not printed: %q", out)
			}
			if !strings.Contains(out, "the sign-in needs API key and standard input has no more lines:") {
				t.Errorf("the end-of-input error was not printed: %q", out)
			}
		})
		t.Run("Ctrl-C", func(t *testing.T) {
			p := pasteWrong(t)
			p.signal(t, syscall.SIGINT)
			p.waitExit(t, 1)
			p.waitEcho(true)
			p.wantChild(t, "code=1", "cancelled=1", "inputs=1")
		})
	})
	// --json on a terminal is a usage error and nothing is read: a silent wait for a line is not a
	// prompt.
	t.Run("json", func(t *testing.T) {
		p := startPTY(t, ptyJSON)
		p.waitExit(t, 2)
		p.wantChild(t, "code=2", "inputs=0")
	})
}

// ptyChild is the command's run: its standard input is the terminal the parent holds, and what it
// and core did is written to the parent as one line, since the parent cannot see core.
func ptyChild(t *testing.T, scenario string) {
	f := newFakeAccountCore(t)
	if scenario == ptySlowCancel {
		f.deleteDelay = 30 * time.Second
	}
	method := "secret_prompt"
	if scenario == ptySwitch {
		method = "switch"
	}
	switch scenario {
	case ptyPaste:
		method = "service_account"
	case ptyBack:
		method = "back"
	case ptyStartTerm:
		method = "api_key"
	case ptyStartOptEOF:
		method = "opt_secret"
	case ptyAddress:
		method = "browser"
	case ptyAddressSecret:
		method = "browser"
		shortLoginWait(t)
	}
	args := []string{"account", "add", "test/ext", "--id", "pty-1", "--method", method,
		"--url", f.srv.URL, "--key", "k"}
	if scenario == ptyJSON {
		args = append(args, "--json")
	}
	var stderr io.Writer = os.Stderr
	echoStayedOn := true
	switch scenario {
	case ptyAddressSecret:
		var login *fakeAccountLogin
		f.onCreate = func(l *fakeAccountLogin) { login = l }
		stderr = &watchWriter{
			w:      os.Stderr,
			needle: browserWaitNotice,
			onMatch: func() {
				f.mu.Lock()
				if login != nil {
					login.prompt = fakeSwitchPrompt()
				}
				f.mu.Unlock()
			},
			afterMatch: func() {
				if !ptyEcho(t, os.Stdin) {
					echoStayedOn = false
				}
			},
		}
	case ptyStartOptEOF:
		stderr = &watchWriter{
			w:       os.Stderr,
			needle:  "Note: ",
			onMatch: func() {},
			afterMatch: func() {
				if !ptyEcho(t, os.Stdin) {
					echoStayedOn = false
				}
			},
		}
	}
	var out bytes.Buffer
	code := runCLIWithInput(args, os.Stdin, &out, stderr, func(string) string { return "" }, true)
	f.mu.Lock()
	summary := fmt.Sprintf("%s code=%d cancelled=%d inputs=%d", ptyMarker, code, len(f.cancelled), len(f.inputs))
	if scenario == ptyAddressSecret || scenario == ptyStartOptEOF {
		if scenario == ptyStartOptEOF {
			summary += fmt.Sprintf(" starts=%d", len(f.starts))
		}
		if echoStayedOn {
			summary += " echo=on"
		} else {
			summary += " echo=off"
		}
	}
	for _, in := range f.inputs {
		for name, value := range in {
			answer := "answer"
			if value != os.Getenv(ptySecretEnv) {
				answer = "other"
			}
			summary += fmt.Sprintf(" input=%s:%s", name, answer)
		}
	}
	if len(f.starts) == 1 && fakeInput(f.starts[0])["service_account"] == os.Getenv(ptySecretEnv) {
		summary += " start=answer"
	}
	f.mu.Unlock()
	fmt.Fprintln(os.Stderr, summary)
	os.Exit(code)
}

// ptySession is one run of the command on a terminal of its own.
type ptySession struct {
	t      *testing.T
	cmd    *exec.Cmd
	master *os.File
	secret string
	// echoed is what the terminal showed, and child what the command itself wrote.
	echoed, child *syncBuffer
	done          chan error
}

// startPTY starts the command in a child process with a pty as its standard input.
func startPTY(t *testing.T, scenario string) *ptySession {
	t.Helper()
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("a terminal: %v", err)
	}
	secret := ptyValue(t)
	if scenario == ptyPaste {
		secret = ptyJSONKey(secret)
	}
	p := &ptySession{t: t, master: master, secret: secret, echoed: &syncBuffer{}, child: &syncBuffer{}, done: make(chan error, 1)}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd = exec.Command(os.Args[0], "-test.run=^TestCLIAccountTerminal$")
	p.cmd.Env = append(os.Environ(), ptyChildEnv+"="+scenario, ptySecretEnv+"="+p.secret)
	p.cmd.Stdin = slave
	p.cmd.Stdout, p.cmd.Stderr = write, write
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("the command: %v", err)
	}
	write.Close()
	// The slave stays open here, so the terminal's settings outlive the child and can be read
	// after it exits.
	t.Cleanup(func() {
		if p.cmd.ProcessState == nil {
			_ = p.cmd.Process.Kill()
		}
		slave.Close()
		master.Close()
		read.Close()
	})
	go func() {
		var buf [4096]byte
		for {
			n, err := master.Read(buf[:])
			if n > 0 {
				p.echoed.add(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		var buf [4096]byte
		for {
			n, err := read.Read(buf[:])
			if n > 0 {
				p.child.add(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { p.done <- p.cmd.Wait() }()
	return p
}

// ptyValue is a value the test types: unlike a fixed string, no run can be mistaken for another's.
func ptyValue(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("fake-key-%x", time.Now().UnixNano())
}

// ptyJSONKey is a made-up pretty-printed key of 13 lines and about 2,200 bytes, each of whose
// values holds the run's own value, so a line that reaches the screen can be recognised.
func ptyJSONKey(value string) string {
	lines := []string{"{"}
	for i := range 11 {
		comma := ","
		if i == 10 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("  \"field_%02d\": \"%s-%02d-%s\"%s", i, value, i, strings.Repeat("x", 150), comma))
	}
	return strings.Join(append(lines, "}"), "\n")
}

// waitOutput waits until the command has written text: a person answers what they can read.
func (p *ptySession) waitOutput(text string) {
	p.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(p.childOutput(), text) {
		if time.Now().After(deadline) {
			p.t.Fatalf("the command did not write %q; it wrote %q", text, p.childOutput())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitEcho waits until the terminal echoes what is typed or stops doing so.
func (p *ptySession) waitEcho(on bool) {
	p.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if ptyEcho(p.t, p.master) == on {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("the terminal's echo is still %v, want %v; the command wrote %q", !on, on, p.childOutput())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitExit waits for the command to end with code.
func (p *ptySession) waitExit(t *testing.T, code int) {
	t.Helper()
	select {
	case err := <-p.done:
		var exited *exec.ExitError
		got := 0
		switch {
		case errors.As(err, &exited):
			got = exited.ExitCode()
		case err != nil:
			t.Fatalf("the command: %v", err)
		}
		if got != code {
			t.Fatalf("the command exited %d, want %d; it wrote %q", got, code, p.childOutput())
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("the command did not exit; it wrote %q", p.childOutput())
	}
}

func (p *ptySession) write(t *testing.T, typed string) {
	t.Helper()
	if _, err := p.master.Write([]byte(typed)); err != nil {
		t.Fatalf("typing %q: %v", typed, err)
	}
}

func (p *ptySession) signal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(p.cmd.Process.Pid, sig); err != nil {
		t.Fatalf("sending %v: %v", sig, err)
	}
}

// echoed is what the terminal showed of what was typed.
func (p *ptySession) echoedText() string { return p.echoed.text() }

// childOutput is what the command wrote on its standard error and output, and core with it.
func (p *ptySession) childOutput() string { return p.child.text() }

// wantSecretKept says the terminal never showed the value that was typed into it.
func (p *ptySession) wantSecretKept(t *testing.T) {
	t.Helper()
	if shown := p.echoedText(); strings.Contains(shown, p.secret) {
		t.Errorf("the terminal showed the secret as it was typed: %q", shown)
	}
}

// wantChild says what the child's line reports: the exit code, the sign-ins cancelled, and each
// answer core was sent (by name, and whether it was the value the test typed).
func (p *ptySession) wantChild(t *testing.T, parts ...string) {
	t.Helper()
	// The command's last line comes through a pipe another goroutine reads, so it can still be on
	// its way when the process has already exited.
	var out, line string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		out = p.childOutput()
		if i := strings.LastIndex(out, ptyMarker); i >= 0 && strings.Contains(out[i:], "\n") {
			line = strings.SplitN(out[i:], "\n", 2)[0]
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if line == "" {
		t.Fatalf("the command reported nothing; it wrote %q", out)
	}
	for _, part := range parts {
		if !strings.Contains(line, part) {
			t.Errorf("the command reported %q, want %q", line, part)
		}
	}
}

// syncBuffer is a buffer two goroutines write and read.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) add(p []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Write(p)
}

func (b *syncBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// watchWriter forwards writes to w, calls onMatch once when the written output contains needle,
// and calls afterMatch on every subsequent write.
type watchWriter struct {
	w          io.Writer
	needle     string
	onMatch    func()
	afterMatch func()
	buf        bytes.Buffer
	matched    bool
}

func (w *watchWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if w.matched {
		if w.afterMatch != nil {
			w.afterMatch()
		}
		return n, err
	}
	w.buf.Write(p[:n])
	if strings.Contains(w.buf.String(), w.needle) {
		w.matched = true
		w.onMatch()
	}
	return n, err
}

// openPTY opens a terminal pair: the master, which is what the terminal shows and what is typed
// into it, and the slave, which is the command's standard input.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	var unlock int32
	if err := ptyIoctl(master, syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		master.Close()
		return nil, nil, err
	}
	var n uint32
	if err := ptyIoctl(master, syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		master.Close()
		return nil, nil, err
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}

// ptyEcho reports whether the terminal echoes what is typed into it, as the kernel has it.
func ptyEcho(t *testing.T, f *os.File) bool {
	t.Helper()
	var tio syscall.Termios
	if err := ptyIoctl(f, syscall.TCGETS, unsafe.Pointer(&tio)); err != nil {
		t.Fatalf("reading the terminal's settings: %v", err)
	}
	return tio.Lflag&syscall.ECHO != 0
}

func ptyIoctl(f *os.File, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}
