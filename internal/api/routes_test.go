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

// registrarMethods are the methods that put a path on a mux. Naming them
// rather than resolving types: go/types would settle what the receiver
// actually is, and it is a dependency this repository does not carry for one
// test. The cost is that an unrelated method called Handle anywhere in this
// package trips this test, and the answer then is to rename it. Loosening the
// check would let the cases it exists for through with it.
var registrarMethods = []string{"Handle", "HandleFunc"}

// sole is the only function that may reach one, and it may only do so while
// ranging over [routes].
//
// Three things are checked and none is enough alone. The count catches a
// second registration; a helper wrapping mux.HandleFunc holds the count at one
// while registering whatever its callers pass. The enclosing function catches
// that helper; a method named Handler on some other type passes a check that
// reads only the name. And both together still miss a slice built from routes
// and something else, which is why the loop has to range over the call itself.
const sole = "Handler"

type mention struct {
	in     string
	inLoop bool
	at     string
}

// Test files are left out. A test building a mux of its own is not a route an
// instance serves, and counting it would make the honest thing fail.
//
// What this does not reach: a route another package registers on a mux handed
// to it, a middleware answering a path itself without registering at all,
// http.StripPrefix carrying a whole sub-handler beneath one registered path,
// and reflection reaching a method by name. Sibling packages give this one an
// http.Handler and never a *http.ServeMux, which is a rule a reviewer keeps.
// So is the rest of that list.
//
// Build tags are not read either. A file this package excludes from the build
// on some platform is still parsed here, so a registrar inside one would fail
// this test on every platform. Nothing in this repository is built that way
// yet.

func TestRoutes_OnlyHandlerRegisters_AndOnlyFromTheTable(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no source files found; this test reads the package it lives in")
	}

	var found []mention
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		loops := overTheTable(file)
		for _, decl := range file.Decls {
			in := ""
			// Recv nil: a method carries its own name, and a method called
			// Handler on another type is not the function this names.
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				in = fn.Name.Name
			}
			// Every selector, not only the ones being called: `f :=
			// mux.HandleFunc` is no call here and registers all the same.
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !slices.Contains(registrarMethods, sel.Sel.Name) {
					return true
				}
				found = append(found, mention{
					in:     in,
					inLoop: within(loops, sel.Pos()),
					at:     fset.Position(sel.Pos()).String(),
				})
				return true
			})
		}
	}

	if len(found) != 1 {
		t.Fatalf("%d mentions of %s, want 1: %s",
			len(found), strings.Join(registrarMethods, " or "), show(found))
	}
	if found[0].in != sole {
		t.Errorf("%s reaches a registrar; only %s may: %s", found[0].in, sole, show(found))
	}
	if !found[0].inLoop {
		t.Errorf("the registrar is reached outside `for range routes(...)`, so what it "+
			"registers is not what the table holds: %s", show(found))
	}
}

// overTheTable returns the body of every `for ... range routes(...)`. Ranging
// over anything else, a slice appended to for instance, registers something
// the table does not hold.
func overTheTable(file *ast.File) [][2]token.Pos {
	var bodies [][2]token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		loop, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		call, ok := loop.X.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "routes" {
			bodies = append(bodies, [2]token.Pos{loop.Body.Pos(), loop.Body.End()})
		}
		return true
	})
	return bodies
}

func within(bodies [][2]token.Pos, at token.Pos) bool {
	for _, b := range bodies {
		if at >= b[0] && at < b[1] {
			return true
		}
	}
	return false
}

func show(ms []mention) string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.in+" at "+m.at)
	}
	return strings.Join(out, ", ")
}
