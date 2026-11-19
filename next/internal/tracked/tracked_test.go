// Package tracked checks the files git tracks under next/: built programs stay out of git, and
// scripts that are run directly keep their run bit.
package tracked

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A program built in place (go build in an extension's folder names it after the folder) is easy to
// commit by mistake, and it can carry what it was built with, such as an OAuth client secret. A
// script committed without its run bit fails only when CI runs it.
func TestTrackedFiles(t *testing.T) {
	root := filepath.Join("..", "..")
	// Outside a git checkout of pluxy (a source archive, or next/ copied into another repository)
	// there is nothing to check; in CI there must be.
	if prefix, err := git(root, "rev-parse", "--show-prefix"); err != nil || strings.TrimSpace(string(prefix)) != "next/" {
		why := "not a git checkout of pluxy's next/"
		if err != nil {
			why += ": " + err.Error()
		}
		if os.Getenv("CI") != "" {
			t.Fatal(why)
		}
		t.Skip(why)
	}
	out, err := git(root, "ls-files", "--stage", "-z", "--", ".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range bytes.Split(out, []byte{0}) {
		// <mode> <object> <stage>\t<path>
		meta, path, ok := bytes.Cut(entry, []byte{'\t'})
		if !ok {
			continue
		}
		mode := string(bytes.Fields(meta)[0])
		if mode == "120000" || mode == "160000" { // symbolic links and submodules have no content here
			continue
		}
		name := "next/" + string(path)
		head, err := readHead(filepath.Join(root, string(path)))
		if errors.Is(err, os.ErrNotExist) {
			continue // outside a sparse checkout, or deleted and not yet staged
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		checked++
		if kind := builtProgram(head); kind != "" {
			t.Errorf("%s is a built %s program. From the repository root: git rm --cached %q, and list it in "+
				"next/.gitignore if a build puts it there", name, kind, name)
		}
		if isScript(head) && mode != "100755" {
			t.Errorf("%s starts with #! but git does not mark it executable (mode %s). From the repository "+
				"root: git update-index --chmod=+x %q", name, mode, name)
		}
	}
	if checked == 0 {
		t.Fatalf("git lists no files under %s", root)
	}
	t.Logf("checked %d tracked files", checked)
}

// git runs git in dir, ignoring a GIT_DIR or GIT_WORK_TREE the test inherited, which would point it
// at another repository or root.
func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_DIR=") && !strings.HasPrefix(kv, "GIT_WORK_TREE=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		err = errors.New(strings.TrimSpace(string(exit.Stderr)))
	}
	if err != nil {
		return nil, errors.New("git " + strings.Join(args, " ") + ": " + err.Error())
	}
	return out, nil
}

// readHead returns up to the first 4 KiB of the file at path.
func readHead(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return head[:n], nil
}

// isScript reports whether head starts with an interpreter line: #!, optional blanks, then a path.
// "#![" (a Rust inner attribute) is not one.
func isScript(head []byte) bool {
	rest, ok := bytes.CutPrefix(head, []byte("#!"))
	return ok && bytes.HasPrefix(bytes.TrimLeft(rest, " \t"), []byte("/"))
}

// builtProgram names the executable format head starts with, or returns "".
func builtProgram(head []byte) string {
	if len(head) < 8 {
		return ""
	}
	switch magic := binary.BigEndian.Uint32(head); {
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		return "ELF"
	case bytes.HasPrefix(head, []byte("\x00asm\x01\x00\x00\x00")):
		return "WebAssembly"
	case magic == 0xfeedface || magic == 0xfeedfacf || magic == 0xcefaedfe || magic == 0xcffaedfe:
		return "Mach-O"
	case magic == 0xcafebabe || magic == 0xcafebabf:
		// A universal Mach-O counts its architectures here; a Java class file has its version (45 or
		// more) in the same place.
		if n := binary.BigEndian.Uint32(head[4:]); n >= 1 && n <= 20 {
			return "universal Mach-O"
		}
	case len(head) >= 0x40 && bytes.HasPrefix(head, []byte("MZ")):
		// A PE file has "PE\0\0" at the offset its DOS header gives.
		off := uint64(binary.LittleEndian.Uint32(head[0x3c:]))
		if off+4 <= uint64(len(head)) && bytes.Equal(head[off:off+4], []byte("PE\x00\x00")) {
			return "Windows (PE)"
		}
	}
	return ""
}

func TestDetectors(t *testing.T) {
	pe := make([]byte, 0x48)
	copy(pe, "MZ")
	binary.LittleEndian.PutUint32(pe[0x3c:], 0x40)
	copy(pe[0x40:], "PE\x00\x00")
	badOffset := bytes.Clone(pe)
	binary.LittleEndian.PutUint32(badOffset[0x3c:], 0xffffffff)
	for _, c := range []struct {
		name, want string
		head       []byte
	}{
		{"ELF", "ELF", []byte("\x7fELF\x02\x01\x01\x00")},
		{"wasm", "WebAssembly", []byte("\x00asm\x01\x00\x00\x00")},
		{"Mach-O 64-bit little-endian", "Mach-O", []byte("\xcf\xfa\xed\xfe\x07\x00\x00\x01")},
		{"universal Mach-O, two architectures", "universal Mach-O", []byte("\xca\xfe\xba\xbe\x00\x00\x00\x02")},
		{"Java class file, version 61", "", []byte("\xca\xfe\xba\xbe\x00\x00\x00\x3d")},
		{"PE", "Windows (PE)", pe},
		{"MZ with an offset past the end", "", badOffset},
		{"PNG", "", []byte("\x89PNG\r\n\x1a\n")},
		{"text", "", []byte("package tracked\n")},
	} {
		if got := builtProgram(c.head); got != c.want {
			t.Errorf("builtProgram(%s) = %q, want %q", c.name, got, c.want)
		}
	}
	for head, want := range map[string]bool{
		"#!/usr/bin/env bun\n": true, "#! /bin/sh\n": true, "#!\t/bin/sh\n": true,
		"#![cfg(debug_assertions)]\n": false, "// #!/bin/sh\n": false, "#!bun\n": false,
	} {
		if got := isScript([]byte(head)); got != want {
			t.Errorf("isScript(%q) = %v, want %v", head, got, want)
		}
	}
}
