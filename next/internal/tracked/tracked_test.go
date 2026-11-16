// Package tracked checks the files git tracks under next/: built programs stay out of git, and
// scripts that are run directly keep their run bit.
package tracked

import (
	"bytes"
	"encoding/binary"
	"errors"
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
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed:", err)
	}
	root := filepath.Join("..", "..")
	out, err := exec.Command("git", "-C", root, "ls-files", "--stage", "-z", "--", ".").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			err = errors.New(strings.TrimSpace(string(exit.Stderr)))
		}
		t.Fatalf("git ls-files in %s: %v", root, err)
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
		name := string(path)
		head := make([]byte, 4096)
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		n, _ := f.Read(head)
		f.Close()
		head = head[:n]
		checked++
		if kind := builtProgram(head); kind != "" {
			t.Errorf("next/%s is a built %s program: remove it from git (git rm --cached next/%s) and list it in "+
				"next/.gitignore", name, kind, name)
		}
		if (bytes.HasPrefix(head, []byte("#!/")) || bytes.HasPrefix(head, []byte("#! /"))) && mode != "100755" {
			t.Errorf("next/%s starts with #! but git does not mark it executable (mode %s): "+
				"git update-index --chmod=+x next/%s", name, mode, name)
		}
	}
	if checked == 0 {
		t.Fatalf("git lists no files under %s", root)
	}
	t.Logf("checked %d tracked files", checked)
}

// builtProgram names the executable format head starts with, or returns "".
func builtProgram(head []byte) string {
	switch {
	case bytes.HasPrefix(head, []byte("\x7fELF")):
		return "ELF"
	case len(head) >= 4 && isMachO(binary.BigEndian.Uint32(head)):
		return "Mach-O"
	case len(head) >= 0x40 && bytes.HasPrefix(head, []byte("MZ")):
		// A PE file has "PE\0\0" at the offset its DOS header gives.
		off := int(binary.LittleEndian.Uint32(head[0x3c:]))
		if off+4 <= len(head) && bytes.Equal(head[off:off+4], []byte("PE\x00\x00")) {
			return "Windows (PE)"
		}
	}
	return ""
}

func isMachO(magic uint32) bool {
	switch magic {
	case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe: // 32- and 64-bit, either byte order
		return true
	}
	return false
}
