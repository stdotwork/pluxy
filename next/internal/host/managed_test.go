//go:build unix

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stdotwork/pluxy/next/internal/configdoc"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol"
)

func prepareFake(t *testing.T, mode string, idle time.Duration) (*Managed, string) {
	t.Helper()
	dir := fakePackage(t, mode)
	p := DefaultPolicy()
	p.IdleStop = idle
	g, err := Prepare(filepath.Join(dir, ManifestFile), 1, p, Options{Log: io.Discard, SettingsSecrets: configdoc.SettingsSecrets})
	if err != nil {
		t.Fatal(err)
	}
	g.logf = t.Logf
	t.Cleanup(g.Stop)
	return g, dir
}

func waitState(t *testing.T, g *Managed, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for g.State() != want {
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s", g.State(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func generateOnce(g *Managed) llm.Event {
	s := g.Generate(context.Background(), protocol.Account{ID: "a"}, llm.Request{Model: "m"})
	defer s.Close()
	return s.Next()
}

// A generation that starts again after an idle stop runs the code it was prepared with: files
// changed in place since then are refused, not run under the old generation.
func TestManagedRestartRunsOnlyItsOwnCode(t *testing.T) {
	g, dir := prepareFake(t, "serve", 20*time.Millisecond)
	waitState(t, g, StateIdle)
	path := filepath.Join(dir, ManifestFile)
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(b), `"0.0.0"`, `"0.0.1"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if !g.Acquire() {
		t.Fatal("Acquire failed")
	}
	defer g.Release()
	if ev := generateOnce(g); ev.Type != llm.EventError || !strings.Contains(ev.Error.Message, "files changed") {
		t.Fatalf("first event after the package changed = %+v; want an error saying its files changed", ev)
	}
}

// Once a generation drains, it takes no new leases, so a late request moves to the newer one
// instead of being cut when the drain ends.
func TestManagedDrainTakesNoNewLeases(t *testing.T) {
	g, _ := prepareFake(t, "serve", 0)
	if !g.Acquire() {
		t.Fatal("Acquire failed")
	}
	g.Drain(func() {})
	if g.Acquire() {
		g.Release()
		t.Error("Acquire succeeded on a draining generation")
	}
	g.Release()
}

// Requests that arrive together at an idle generation all wait for one start.
func TestManagedConcurrentLazyStarts(t *testing.T) {
	g, _ := prepareFake(t, "serve", 20*time.Millisecond)
	waitState(t, g, StateIdle)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			if !g.Acquire() {
				errs <- "Acquire failed"
				return
			}
			defer g.Release()
			if ev := generateOnce(g); ev.Type == llm.EventError {
				errs <- ev.Error.Message
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent cold request failed: %s", e)
	}
}

// Stop returns only once every program of the generation has exited, including one an idle
// stop is still stopping.
func TestManagedStopWaitsForIdleStop(t *testing.T) {
	g, _ := prepareFake(t, "slow-exit", 20*time.Millisecond)
	pid := g.Status().PID
	waitState(t, g, StateIdle) // the program is asked to stop and takes 500ms to exit
	g.Stop()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("process %d still exists after Stop returned (signal 0: %v)", pid, err)
	}
}
