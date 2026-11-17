package corpus

import (
	"os"
	"strings"
	"testing"
)

func TestOldCoreBinary(t *testing.T) {
	if bin, err := oldCoreBinary(false, ""); bin != "" || err != nil {
		t.Errorf("recorded run: got %q, %v; want no binary and no error", bin, err)
	}
	if bin, err := oldCoreBinary(true, "/x/old-core"); bin != "/x/old-core" || err != nil {
		t.Errorf("live run with a binary: got %q, %v; want /x/old-core and no error", bin, err)
	}
	_, err := oldCoreBinary(true, "")
	if err == nil {
		t.Fatal("a live run with no old-core binary was accepted")
	}
	for _, want := range []string{"PLUXY_CORPUS_OLD_CORE_BIN", oldCoreReadmeSection} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
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
