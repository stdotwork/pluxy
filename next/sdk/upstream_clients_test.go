package sdk

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep production provider traffic behind the SDK seam, including login and WebSocket
// handshakes, in the extensions and in the examples extension authors copy. Fake providers and
// test-only clients deliberately need no wrapper.
func TestExtensionUpstreamClients(t *testing.T) {
	files, clients := 0, 0
	for _, root := range []string{filepath.Join("..", "extensions"), filepath.Join("..", "examples")} {
		f, c := checkUpstreamClients(t, root)
		files, clients = files+f, clients+c
		if f == 0 || c == 0 {
			t.Errorf("%s: checked %d files and %d clients; expected production extensions", root, f, c)
		}
	}
	t.Logf("checked %d production files and %d clients", files, clients)
}

// checkUpstreamClients reports each http client under root that is not wrapped in sdk.Upstream,
// and returns how many files and clients it checked.
func checkUpstreamClients(t *testing.T, root string) (files, clients int) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasSuffix(entry.Name(), "test") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if strings.HasSuffix(file.Name.Name, "test") {
			return nil
		}
		files++
		names := map[string]string{}
		for _, spec := range file.Imports {
			pkg, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			name := filepath.Base(pkg)
			if spec.Name != nil {
				name = spec.Name.Name
			}
			names[name] = pkg
		}
		selector := func(expr ast.Expr, pkg, member string) bool {
			s, ok := expr.(*ast.SelectorExpr)
			if !ok || s.Sel.Name != member {
				return false
			}
			id, ok := s.X.(*ast.Ident)
			return ok && names[id.Name] == pkg
		}
		wrapped := func(expr ast.Expr) bool {
			call, ok := expr.(*ast.CallExpr)
			return ok && selector(call.Fun, "github.com/stdotwork/pluxy/next/sdk", "Upstream") && len(call.Args) == 1
		}
		// The shared default may only be referenced to clone it inside sdk.Upstream.
		clonedDefaults := map[ast.Node]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !wrapped(call) {
				return true
			}
			clone, ok := call.Args[0].(*ast.CallExpr)
			if !ok || len(clone.Args) != 0 {
				return true
			}
			method, ok := clone.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "Clone" {
				return true
			}
			assertion, ok := method.X.(*ast.TypeAssertExpr)
			if !ok {
				return true
			}
			ptr, ok := assertion.Type.(*ast.StarExpr)
			if ok && selector(ptr.X, "net/http", "Transport") && selector(assertion.X, "net/http", "DefaultTransport") {
				clonedDefaults[assertion.X] = true
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			if expr, ok := n.(*ast.SelectorExpr); ok && (selector(expr, "net/http", "DefaultClient") || selector(expr, "net/http", "DefaultTransport")) && !clonedDefaults[expr] {
				t.Errorf("%s: bare http.%s bypasses sdk.Upstream", fset.Position(expr.Pos()), expr.Sel.Name)
			}
			unary, ok := n.(*ast.UnaryExpr)
			if !ok || unary.Op != token.AND {
				return true
			}
			lit, ok := unary.X.(*ast.CompositeLit)
			if !ok || !selector(lit.Type, "net/http", "Client") {
				return true
			}
			clients++
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if ok && key.Name == "Transport" && wrapped(kv.Value) {
					return true
				}
			}
			t.Errorf("%s: http.Client Transport must call sdk.Upstream", fset.Position(unary.Pos()))
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, clients
}
