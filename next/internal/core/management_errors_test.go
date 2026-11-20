package core

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The management API's error codes are documented in ../../management/README.md. Its handlers give
// them in a few shapes, collected here from the source of the two packages that serve the API
// (this one and internal/server): the code argument of writeError and writeManagementError, the
// code field of an editError literal, and the code of the {"error": {"code": ...}} body they
// write. A handler that starts giving a new code, or the README naming one no handler gives, fails
// this test instead of drifting.
//
// A code forwarded from an extension's or a provider's error is not in the source as a literal:
// writeCredError passes on an llm.Error's code, and writeAccountError writes route.Brief's. Such a
// code can be any of the ones llm declares, so the test then requires the table to list them all.
// Anything else it cannot read fails rather than being skipped.

const (
	managementReadme = "../../management/README.md"
	llmPackageDir    = "../../llm"
)

func TestManagementErrorTable(t *testing.T) {
	documented, err := documentedErrorCodes(managementReadme)
	if err != nil {
		t.Fatal(err)
	}
	llmConsts, err := llmCodeConsts(llmPackageDir)
	if err != nil {
		t.Fatal(err)
	}
	given, err := handlerErrorCodes(t, llmConsts, ".", "../server")
	if err != nil {
		t.Fatal(err)
	}
	if given.forwarded {
		for _, c := range llmConsts {
			given.codes[c] = true
		}
	}
	for _, c := range missingCodes(given.codes, documented) {
		t.Errorf("core answers %q, and the README's table does not list it", c)
	}
	for _, c := range missingCodes(documented, given.codes) {
		t.Errorf("the README's table lists %q, and no handler answers it", c)
	}
}

func missingCodes(want, have map[string]bool) []string {
	out := []string{}
	for c := range want {
		if !have[c] {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// codeToken is the table's spelling of a code: a word in backticks.
var codeToken = regexp.MustCompile("`([^`]+)`")

// documentedErrorCodes returns the codes of the README's "| Status | Codes |" table.
func documentedErrorCodes(path string) (map[string]bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	codes := map[string]bool{}
	inTable := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "| Status | Codes |") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(line, "|") {
			break
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		for _, m := range codeToken.FindAllStringSubmatch(cells[2], -1) {
			codes[m[1]] = true
		}
	}
	if len(codes) == 0 {
		return nil, fmt.Errorf("%s: no error code table found", path)
	}
	return codes, nil
}

func noTests(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }

// stringConsts returns the package's constants whose value is a string.
func stringConsts(pkg *ast.Package) map[string]string {
	out := map[string]string{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						if v, err := strconv.Unquote(bl.Value); err == nil {
							out[name.Name] = v
						}
					}
				}
			}
		}
	}
	return out
}

// handlerCodes is what the handlers were found to answer.
type handlerCodes struct {
	fset  *token.FileSet
	codes map[string]bool
	// forwarded is true when a handler passes on an extension's or provider's error code.
	forwarded bool
}

// handlerErrorCodes walks the non-test Go files of dirs, which must be packages serving the
// management API. llmConsts is llm's error codes, by constant name.
func handlerErrorCodes(t *testing.T, llmConsts map[string]string, dirs ...string) (*handlerCodes, error) {
	t.Helper()
	c := &handlerCodes{fset: token.NewFileSet(), codes: map[string]bool{}}
	for _, dir := range dirs {
		pkgs, err := parser.ParseDir(c.fset, dir, noTests, 0)
		if err != nil {
			return nil, err
		}
		for _, pkg := range pkgs {
			consts := stringConsts(pkg)
			for _, f := range pkg.Files {
				for _, d := range f.Decls {
					fd, ok := d.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					switch fd.Name.Name {
					case "writeError", "writeManagementError":
						continue // the helpers that write every error body
					}
					vars, maps := assignedLiterals(fd.Body)
					c.walk(t, fd.Body, consts, llmConsts, vars, maps)
				}
			}
		}
	}
	return c, nil
}

// llmCodeConsts returns llm's constants that are error codes, by name.
func llmCodeConsts(dir string) (map[string]string, error) {
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, noTests, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, pkg := range pkgs {
		for name, value := range stringConsts(pkg) {
			if strings.HasPrefix(name, "Code") {
				out[name] = value
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no Code constants found", dir)
	}
	return out, nil
}

// assignedLiterals returns, for one function body, the string literals assigned to each variable
// and the composite literals assigned to each.
func assignedLiterals(body *ast.BlockStmt) (map[string][]string, map[string]*ast.CompositeLit) {
	vars := map[string][]string{}
	maps := map[string]*ast.CompositeLit{}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, l := range as.Lhs {
			id, ok := l.(*ast.Ident)
			if !ok {
				continue
			}
			switch v := as.Rhs[i].(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if s, err := strconv.Unquote(v.Value); err == nil {
						vars[id.Name] = append(vars[id.Name], s)
					}
				}
			case *ast.CompositeLit:
				maps[id.Name] = v
			}
		}
		return true
	})
	return vars, maps
}

func (c *handlerCodes) walk(t *testing.T, body *ast.BlockStmt, consts, llmConsts map[string]string, vars map[string][]string, maps map[string]*ast.CompositeLit) {
	t.Helper()
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			fn, ok := x.Fun.(*ast.Ident)
			if !ok || len(x.Args) < 3 {
				return true
			}
			switch fn.Name {
			case "writeError", "writeManagementError":
				c.add(t, x.Args[2], consts, llmConsts, vars)
			case "writeJSON":
				arg := x.Args[2]
				if id, ok := arg.(*ast.Ident); ok && maps[id.Name] != nil {
					arg = maps[id.Name]
				}
				lit, ok := arg.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if e, ok := errorValue(lit); ok {
					c.add(t, e, consts, llmConsts, vars)
				}
			}
		case *ast.CompositeLit:
			if id, ok := x.Type.(*ast.Ident); ok && id.Name == "editError" && len(x.Elts) >= 2 {
				c.add(t, x.Elts[1], consts, llmConsts, vars)
			}
		}
		return true
	})
}

// errorValue returns the expression an error body literal gives its code in: the value of the
// "code" key of {"error": {"code": ...}}, or the call that builds the error.
func errorValue(lit *ast.CompositeLit) (ast.Expr, bool) {
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.BasicLit); !ok || k.Value != `"error"` {
			continue
		}
		inner, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return kv.Value, true // a call building the error, as route.Brief does
		}
		for _, e2 := range inner.Elts {
			kv2, ok := e2.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k2, ok := kv2.Key.(*ast.BasicLit); ok && k2.Value == `"code"` {
				return kv2.Value, true
			}
		}
	}
	return nil, false
}

// add records the code the expression e gives.
func (c *handlerCodes) add(t *testing.T, e ast.Expr, consts, llmConsts map[string]string, vars map[string][]string) {
	t.Helper()
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil {
				c.codes[s] = true
				return
			}
		}
	case *ast.Ident:
		if s, ok := consts[v.Name]; ok {
			c.codes[s] = true
			return
		}
		if vs, ok := vars[v.Name]; ok {
			for _, s := range vs {
				c.codes[s] = true
			}
			return
		}
	case *ast.SelectorExpr:
		// editError's code field: the literals given to editError are collected where they are
		// given.
		if v.Sel.Name == "code" {
			return
		}
		// llm.CodeAuth and the like name a code; any other field (le.Code, e.Code) carries one
		// from an extension or a provider.
		if base, ok := v.X.(*ast.Ident); ok && base.Name == "llm" {
			if s, ok := llmConsts[v.Sel.Name]; ok {
				c.codes[s] = true
				return
			}
		}
		c.forwarded = true
		return
	case *ast.CallExpr:
		c.forwarded = true
		return
	}
	t.Errorf("%s: cannot read the error code (%T); extend this test to collect it", c.fset.Position(e.Pos()), e)
}
