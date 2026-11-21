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
	ptySecret     = "secret"     // a secret field, typed and answered
	ptySigint     = "sigint"     // Ctrl-C while the secret is being typed
	ptySigterm    = "sigterm"    // SIGTERM at the same point
	ptyHangup     = "hangup"     // SIGHUP at the same point
	ptySwitch     = "switch"     // the prompt changes to a secret field while a plain one is read
	ptyJSON       = "json"       // --json on a terminal, which asks nothing and reads nothing
	ptySlowCancel = "slowcancel" // SIGTERM while core does not answer the cancel at all
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
		p.write(t, "stale-line\n"+p.secret+"\n")
		p.waitExit(t, 0)
		p.waitEcho(true)
		p.wantSecretKept(t)
		if strings.Contains(p.echoedText(), "stale-line") {
			t.Errorf("the line read for the old prompt was echoed: %q", p.echoedText())
		}
		p.wantChild(t, "code=0", "cancelled=0", "input=api_key:answer")
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
	args := []string{"account", "add", "test/ext", "--id", "pty-1", "--method", method,
		"--url", f.srv.URL, "--key", "k"}
	if scenario == ptyJSON {
		args = append(args, "--json")
	}
	var out bytes.Buffer
	code := runCLIWithInput(args, os.Stdin, &out, os.Stderr, func(string) string { return "" }, true)
	f.mu.Lock()
	summary := fmt.Sprintf("%s code=%d cancelled=%d inputs=%d", ptyMarker, code, len(f.cancelled), len(f.inputs))
	for _, in := range f.inputs {
		for name, value := range in {
			answer := "answer"
			if value != os.Getenv(ptySecretEnv) {
				answer = "other"
			}
			summary += fmt.Sprintf(" input=%s:%s", name, answer)
		}
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
	p := &ptySession{t: t, master: master, secret: ptyValue(t), echoed: &syncBuffer{}, child: &syncBuffer{}, done: make(chan error, 1)}
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
	out := p.childOutput()
	line := ""
	if i := strings.LastIndex(out, ptyMarker); i >= 0 {
		line = strings.SplitN(out[i:], "\n", 2)[0]
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
