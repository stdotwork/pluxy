package format_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
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

// parserHash is what TestParserVersion hashes the parsers' output to at format.ParserVersion.
const parserHash = "dc727db32bff745a42651713745c48991d010428b0d46901ecc95fc810f6d738"

// TestParserVersion keeps format.ParserVersion honest: it parses every corpus and format-test
// request as core reads it and then as an extension restores a native-only call from it
// (nativeonly.Parse), and hashes what each makes of it (the items, tools and origin, or that the
// parser refused it). When any parser's output changes, the hash does.
func TestParserVersion(t *testing.T) {
	corpus, err := formattest.CorpusRequests("..")
	if err != nil {
		t.Fatal(err)
	}
	literals, err := formattest.LiteralRequests("..")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, r := range append(corpus, literals...) {
		out, ok := parseAsExtension(r.Path, r.Body)
		if !ok {
			continue
		}
		in := sha256.Sum256([]byte(r.Path + "\n" + string(r.Body)))
		lines = append(lines, hex.EncodeToString(in[:])+" "+out)
	}
	// The same request may be in several places; its order in them does not matter.
	slices.Sort(lines)
	lines = slices.Compact(lines)
	if len(lines) < 500 {
		t.Fatalf("only %d distinct generate requests", len(lines))
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	if got := hex.EncodeToString(sum[:]); got != parserHash {
		t.Fatalf("the format parsers' output on %d requests hashes to %s, not parserHash %s.\n"+
			"If a parser now makes something else of a body (its items, tools or origin, or whether it refuses it), "+
			"raise format.ParserVersion (now %d) and set parserHash to %s, so core sends the full request to "+
			"an extension built with the old parsers. If only the requests changed (a corpus case or a format "+
			"test added or edited), set parserHash alone.", len(lines), got, parserHash, format.ParserVersion, got)
	}
}

// parseAsExtension reads body as core does for a generate request to path and, when core takes
// it, restores it as an extension does from a native-only call. It returns what the extension
// gets, encoded, and false for a path that is not a generate route.
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
