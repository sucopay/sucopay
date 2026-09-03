package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// registrars are the methods that put a path on a mux. Naming them rather than
// resolving types: go/types would settle what the receiver actually is, and it
// is a dependency this repository does not carry for one test. The cost is that
// an unrelated method called Handle anywhere in this package trips this, and
// the answer then is to rename it rather than to loosen the check.
var registrars = []string{"Handle", "HandleFunc"}

// registrar is where a match sits: which function, and where.
type registrar struct {
	in string
	at string
}

// what is which function may register, and it is [Handler] alone. Counting
// calls is not enough on its own: a helper wrapping mux.HandleFunc keeps the
// count at one while registering anything its callers pass, and that helper
// reads as an ordinary way to avoid repeating a line.
const what = "Handler"

// Test files are left out. A test building a mux of its own is not a route an
// instance serves, and counting it would make the honest thing fail.
//
// What this does not reach: a route another package registers on a mux handed
// to it, and a middleware answering a path itself without registering at all.
// Sibling packages give this one an http.Handler and never a *http.ServeMux,
// which is a rule a reviewer keeps rather than this test.

func TestRoutes_NothingRegistersAPathOutsideTheTable(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no source files found; this test reads the package it lives in")
	}

	var found []registrar
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			in := ""
			if fn, ok := decl.(*ast.FuncDecl); ok {
				in = fn.Name.Name
			}
			// Every selector, not only the ones being called: `f :=
			// mux.HandleFunc` registers through f later and is no call here.
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if ok && slices.Contains(registrars, sel.Sel.Name) {
					found = append(found, registrar{in: in, at: fset.Position(sel.Pos()).String()})
				}
				return true
			})
		}
	}

	if len(found) != 1 {
		t.Fatalf("%d mentions of %s, want 1: %s",
			len(found), strings.Join(registrars, " or "), show(found))
	}
	if found[0].in != what {
		t.Errorf("%s reaches a registrar; only %s may: %s", found[0].in, what, show(found))
	}
}

func show(rs []registrar) string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.in+" at "+r.at)
	}
	return strings.Join(out, ", ")
}
