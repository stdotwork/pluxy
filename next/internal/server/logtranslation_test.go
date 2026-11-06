package server

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stdotwork/pluxy/next/llm"
)

// captureLog sends the standard logger's output to a buffer until the test ends.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &out
}

// A request with 10,000 losses, each with a 1 KiB path and detail, logs one short line: the first
// 20 losses with their paths and details cut to 200 bytes, then a count of the rest. Twenty such
// entries come to about 8.6 KiB, so the line is held under 16 KiB.
func TestLogTranslationBoundsTheLine(t *testing.T) {
	out := captureLog(t)
	losses := make([]llm.Loss, 10000)
	for i := range losses {
		losses[i] = llm.Loss{
			Kind:   llm.LossDropped,
			Path:   fmt.Sprintf("f%05d.%s", i, strings.Repeat("x", 1024)),
			Detail: strings.Repeat("d", 1024),
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	logTranslation(r, &llm.Translation{From: llm.FormatOpenAIChat, To: llm.FormatOpenAIResponses, Losses: losses}, nil)
	line := strings.TrimSuffix(out.String(), "\n")
	if len(line) >= 16384 {
		t.Fatalf("log line is %d bytes, want under 16384", len(line))
	}
	for _, want := range []string{"request dropped f00000.", "request dropped f00019.", "; and 9980 more"} {
		if !strings.Contains(line, want) {
			t.Errorf("log line does not contain %q", want)
		}
	}
	if strings.Contains(line, "f00020.") {
		t.Errorf("log line names the 21st loss")
	}
	if strings.Contains(line, strings.Repeat("x", 201)) || strings.Contains(line, strings.Repeat("d", 201)) {
		t.Errorf("a loss's path or detail is not cut to 200 bytes")
	}
}

// A cut falls on a UTF-8 boundary, so the log line stays valid text. "€" is 3 bytes, so 66 whole
// runes fit in 200 bytes and a 67th would not.
func TestLogTranslationCutsOnRuneBoundary(t *testing.T) {
	out := captureLog(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	logTranslation(r, &llm.Translation{Losses: []llm.Loss{{Kind: llm.LossDropped, Path: strings.Repeat("€", 100), Detail: "d"}}}, nil)
	line := strings.TrimSuffix(out.String(), "\n")
	if !utf8.ValidString(line) {
		t.Errorf("log line is not valid UTF-8")
	}
	if want := strings.Repeat("€", 66) + "…"; !strings.Contains(line, want) {
		t.Errorf("log line does not hold the path cut to 66 runes and an ellipsis")
	}
	if strings.Contains(line, strings.Repeat("€", 67)) {
		t.Errorf("log line holds 67 runes of the path, more than 200 bytes")
	}
}

// The request path is client input too, so the log line cuts it like a loss's path: to 200 bytes
// on a UTF-8 boundary.
func TestLogTranslationCutsThePath(t *testing.T) {
	out := captureLog(t)
	r := httptest.NewRequest(http.MethodPost, "/v1/"+strings.Repeat("€", 5000), nil)
	logTranslation(r, &llm.Translation{Losses: []llm.Loss{{Kind: llm.LossDropped, Path: "p", Detail: "d"}}}, nil)
	line := strings.TrimSuffix(out.String(), "\n")
	if len(line) >= 1024 {
		t.Fatalf("log line is %d bytes, want under 1024", len(line))
	}
	if !utf8.ValidString(line) {
		t.Errorf("log line is not valid UTF-8")
	}
	if !strings.Contains(line, "POST /v1/€") || !strings.Contains(line, "…: translation: request dropped p (d)") {
		t.Errorf("log line does not hold the cut path and the loss: %q", line)
	}
}
