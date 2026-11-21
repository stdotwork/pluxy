package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsInteractiveNonTerminal(t *testing.T) {
	devNullIn, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNullIn.Close()
	if isInteractive(devNullIn) {
		t.Errorf("isInteractive(%s) = true, want false", os.DevNull)
	}

	devNullOut, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s for write: %v", os.DevNull, err)
	}
	defer devNullOut.Close()
	if stdoutIsTerminal(devNullOut) {
		t.Errorf("stdoutIsTerminal(%s) = true, want false", os.DevNull)
	}

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer pr.Close()
	defer pw.Close()
	if isInteractive(pr) {
		t.Errorf("isInteractive(pipe read end) = true, want false")
	}
	if stdoutIsTerminal(pw) {
		t.Errorf("stdoutIsTerminal(pipe write end) = true, want false")
	}

	reg, err := os.Create(filepath.Join(t.TempDir(), "file.txt"))
	if err != nil {
		t.Fatalf("create regular file: %v", err)
	}
	defer reg.Close()
	if isInteractive(reg) {
		t.Errorf("isInteractive(regular file) = true, want false")
	}
	if stdoutIsTerminal(reg) {
		t.Errorf("stdoutIsTerminal(regular file) = true, want false")
	}

	if isInteractive(strings.NewReader("")) {
		t.Errorf("isInteractive(strings.Reader) = true, want false")
	}
	var nilFile *os.File
	if isInteractive(nilFile) {
		t.Errorf("isInteractive((*os.File)(nil)) = true, want false")
	}
}
