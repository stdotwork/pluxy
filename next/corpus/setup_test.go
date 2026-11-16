package corpus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckOldCoreDir(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "core")
	err := checkOldCoreDir(missing)
	if err == nil {
		t.Fatal("a missing old-core folder was accepted")
	}
	for _, want := range []string{missing, "PLUXY_CORPUS_OLD_CORE_BIN", oldCoreReadmeSection} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if err := checkOldCoreDir(t.TempDir()); err != nil {
		t.Errorf("an existing old-core folder was refused: %v", err)
	}
}

// The error names a README section; it has to be there.
func TestOldCoreReadmeSectionExists(t *testing.T) {
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "\n### "+oldCoreReadmeSection+"\n") {
		t.Errorf("README.md has no %q heading", oldCoreReadmeSection)
	}
}
