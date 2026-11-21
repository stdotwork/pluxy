//go:build linux

package main

import "testing"

func TestIsInteractivePTY(t *testing.T) {
	master, slave, err := openPTY()
	if err != nil {
		t.Fatalf("openPTY: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	if !isInteractive(slave) {
		t.Errorf("isInteractive(pty slave) = false, want true")
	}
	if !stdoutIsTerminal(slave) {
		t.Errorf("stdoutIsTerminal(pty slave) = false, want true")
	}
}
