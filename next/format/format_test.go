package format_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/format"
	"github.com/stdotwork/pluxy/next/format/anthropicmessages"
	"github.com/stdotwork/pluxy/next/format/formattest"
	"github.com/stdotwork/pluxy/next/format/geminigenerate"
	"github.com/stdotwork/pluxy/next/format/openaichat"
	"github.com/stdotwork/pluxy/next/format/openairesponses"
	"github.com/stdotwork/pluxy/next/llm"
	"github.com/stdotwork/pluxy/next/protocol/nativeonly"
)

const (
	goldenFile      = "testdata/parser_golden.txt"
	goldenUpdateVar = "PLUXY_FORMAT_GOLDEN_UPDATE"
)

func TestParserVersion(t *testing.T) {
	corpus, err := formattest.CorpusRequests("..")
	if err != nil {
		t.Fatal(err)
	}
	literals, err := formattest.LiteralRequests("..")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	names := map[string]string{}
	for _, r := range append(corpus, literals...) {
		out, ok := parseAsExtension(r.Path, r.Body)
		if !ok {
			continue
		}
		key := shortHash([]byte(r.Path + "\n" + string(r.Body)))
		got[key] = shortHash([]byte(out))
		names[key] = r.Source + ": " + r.Name
	}
	if len(got) < 500 {
		t.Fatalf("only %d distinct generate requests", len(got))
	}
	if os.Getenv(goldenUpdateVar) == "1" {
		writeGolden(t, got)
		return
	}
	recorded := readGolden(t)
	var changed []string
	newRequests := 0
	for _, key := range slices.Sorted(maps.Keys(got)) {
		was, ok := recorded[key]
		switch {
		case !ok:
			newRequests++
		case was != got[key]:
			changed = append(changed, names[key])
		}
	}
	if newRequests > 0 {
		t.Logf("%d of %d requests are not in %s; %s=1 records them", newRequests, len(got), goldenFile, goldenUpdateVar)
	}
	if len(changed) > 0 {
		const show = 10
		listed := changed
		more := ""
		if len(listed) > show {
			listed, more = listed[:show], "; and "+strconv.Itoa(len(changed)-show)+" more"
		}
		t.Fatalf("the format parsers now make something else of %d recorded requests:\n  %s%s.\n"+
			"If a parser now makes something else of a body (its items, tools or origin, or whether it refuses it), "+
			"raise format.ParserVersion (now %d) and regenerate %s with %s=1, so core sends the full request "+
			"to an extension built with the old parsers.",
			len(changed), strings.Join(listed, "\n  "), more, format.ParserVersion, goldenFile, goldenUpdateVar)
	}
}

func shortHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func readGolden(t *testing.T) map[string]string {
	t.Helper()
	b, err := os.ReadFile(goldenFile)
	if err != nil {
		t.Fatalf("%v; record it with %s=1", err, goldenUpdateVar)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, was, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("%s: %q is not \"<request> <result>\"", goldenFile, line)
		}
		out[key] = was
	}
	if len(out) < 500 {
		t.Fatalf("%s holds %d requests; record it with %s=1", goldenFile, len(out), goldenUpdateVar)
	}
	return out
}

func writeGolden(t *testing.T, got map[string]string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("# What the format packages' parsers make of requests, so TestParserVersion can tell when\n")
	b.WriteString("# one of them changes: one line per request, the SHA-256 (first 8 bytes, hex) of its route\n")
	b.WriteString("# and body, then of what an extension gets from it (its items, tools and origin, or that\n")
	b.WriteString("# core or the extension refuses it). Recorded at format.ParserVersion " +
		strconv.Itoa(format.ParserVersion) + " by TestParserVersion.\n")
	b.WriteString("#\n")
	b.WriteString("# A request that is not here is not checked, and one that is here but is no longer gathered\n")
	b.WriteString("# is ignored: only a recorded request that parses differently fails the test. Record it\n")
	b.WriteString("# with " + goldenUpdateVar + "=1 go test ./format/ in next/ after a parser change, with\n")
	b.WriteString("# format.ParserVersion raised.\n")
	for _, key := range slices.Sorted(maps.Keys(got)) {
		b.WriteString(key + " " + got[key] + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(goldenFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goldenFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d requests in %s", len(got), goldenFile)
}

func parseAsExtension(path string, body []byte) (string, bool) {
	path, _, _ = strings.Cut(path, "?")
	var req llm.Request
	var err error
	switch {
	case path == "/v1/chat/completions":
		req, err = openaichat.ParseRequest(body)
	case path == "/v1/completions":
		req, err = openaichat.ParseCompletionsRequest(body)
	case path == "/v1/responses" || path == "/backend-api/codex/responses":
		req, err = openairesponses.ParseRequest(body)
	case path == "/v1/messages":
		req, err = anthropicmessages.ParseRequest(body)
	case strings.HasPrefix(path, "/v1beta/models/"):
		model, method, _ := strings.Cut(strings.TrimPrefix(path, "/v1beta/models/"), ":")
		if method != "generateContent" && method != "streamGenerateContent" {
			return "", false
		}
		req, err = geminigenerate.ParseRequest(model, method == "streamGenerateContent", body)
	default:
		return "", false
	}
	if err != nil {
		return "core refuses", true
	}
	if req.Native == nil {
		return "no native body", true
	}
	sent := nativeonly.Strip(req)
	if err := nativeonly.Parse(&sent); err != nil {
		return "extension refuses", true
	}
	b, err := json.Marshal(struct {
		Items  []llm.Item  `json:"items"`
		Tools  []llm.Tool  `json:"tools"`
		Origin *llm.Origin `json:"origin"`
	}{sent.Items, sent.Tools, sent.Origin})
	if err != nil {
		return "does not encode: " + err.Error(), true
	}
	return string(b), true
}
