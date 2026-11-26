// Package formattest gathers the client requests that tests of the format parsers run through
// them: every request the corpus cases send, and every JSON object the format packages' tests
// hold as a string literal, as a request to each route that reads that package's format.
package formattest

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/stdotwork/pluxy/next/corpus"
)

// Request is one client HTTP request.
type Request struct {
	Source  string // "corpus" or "format tests"
	Name    string
	Path    string
	Headers map[string]string
	Body    []byte
}

// CorpusRequests returns the POST requests of every corpus case (client and then), as the case
// files under next (the module's folder) hold them. Not all are generate requests.
func CorpusRequests(next string) ([]Request, error) {
	cases, err := corpus.LoadCases(filepath.Join(next, "corpus", "cases"))
	if err != nil {
		return nil, err
	}
	var out []Request
	for _, c := range cases {
		for i, cr := range append([]corpus.ClientRequest{c.Client}, c.Then...) {
			if cr.WebSocket != nil || (cr.Method != "" && cr.Method != http.MethodPost) {
				continue
			}
			body := []byte(cr.Body)
			if cr.BodyText != nil {
				body = []byte(*cr.BodyText)
			}
			out = append(out, Request{Source: "corpus", Name: c.Format + "/" + c.Provider + "/" + c.Name + "#" + strconv.Itoa(i), Path: cr.Path, Headers: cr.Headers, Body: body})
		}
	}
	if len(out) < 100 {
		return nil, fmt.Errorf("only %d corpus requests", len(out))
	}
	return out, nil
}

// literalRoutes are the routes that read each format package's requests.
var literalRoutes = map[string][]string{
	"openaichat":        {"/v1/chat/completions", "/v1/completions"},
	"openairesponses":   {"/v1/responses"},
	"anthropicmessages": {"/v1/messages"},
	"geminigenerate":    {"/v1beta/models/gemini-x:generateContent", "/v1beta/models/gemini-x:streamGenerateContent"},
}

// LiteralRequests returns every string literal in the format packages' tests under next (the
// module's folder) that is a JSON object, as a request to each route that reads the package's
// format, ordered by name. Not all are requests.
func LiteralRequests(next string) ([]Request, error) {
	var out []Request
	for pkg, routes := range literalRoutes {
		files, err := filepath.Glob(filepath.Join(next, "format", pkg, "*_test.go"))
		if err != nil || len(files) == 0 {
			return nil, fmt.Errorf("no tests in format/%s: %v", pkg, err)
		}
		for _, file := range files {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				return nil, err
			}
			name := filepath.ToSlash(filepath.Join("format", pkg, filepath.Base(file)))
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !strings.HasPrefix(strings.TrimSpace(s), "{") || !json.Valid([]byte(s)) {
					return true
				}
				pos := fset.Position(lit.Pos())
				for _, p := range routes {
					out = append(out, Request{Source: "format tests", Name: fmt.Sprintf("%s:%d:%d %s", name, pos.Line, pos.Column, p), Path: p, Body: []byte(s)})
				}
				return true
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
