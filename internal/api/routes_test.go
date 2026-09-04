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
	// admits is whether the registrar is given admit(x.needs, x.handle) for
	// one x: the table's handler, behind the access the table states for it.
	admits bool
	at     string
}

func TestRoutes_OnlyHandlerRegisters_AndOnlyFromTheTable(t *testing.T) {
	t.Parallel()
	found := registrations(t)

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

// TestRoutes_EveryRouteIsRegisteredBehindTheAccessItStates reads what the
// registrar is given, where the test above settles who reaches it and over
// what. Registering r.handle by itself passes that test and serves the route
// to anyone; so does admit(open, r.handle). No request shows either while
// the table holds only open routes, and the source does.
func TestRoutes_EveryRouteIsRegisteredBehindTheAccessItStates(t *testing.T) {
	t.Parallel()
	for _, m := range registrations(t) {
		if !m.admits {
			t.Errorf("the registrar is given something other than admit(r.needs, r.handle), "+
				"so a route is served past what it asks of a caller: %s at %s", m.in, m.at)
		}
	}
}

// registrations is every mention of a registrar in the package's non-test
// files. Test files are left out. A test building a mux of its own is not a
// route an instance serves, and counting it would make the honest thing
// fail.
//
// What this does not reach: a route another package registers on a mux
// handed to it, a middleware answering a path itself without registering at
// all, http.StripPrefix carrying a whole sub-handler beneath one registered
// path, and reflection reaching a method by name. Sibling packages give this
// one an http.Handler and never a *http.ServeMux, and that is kept in review,
// not here. So is the rest of that list.
//
// Build tags are not read either. A file this package excludes from the
// build on some platform is still parsed here, so a registrar inside one
// would fail these tests on every platform. Nothing in this repository is
// built that way yet.
func registrations(t *testing.T) []mention {
	t.Helper()
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
			// A call is visited before the selector it calls, so what a
			// registrar is given is known by the time its selector is
			// recorded.
			given := map[*ast.SelectorExpr]bool{}
			// Every selector, not only the ones being called: `f :=
			// mux.HandleFunc` is no call here and registers all the same,
			// and is given nothing.
			ast.Inspect(decl, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						given[sel] = admitted(call)
					}
					return true
				}
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !slices.Contains(registrarMethods, sel.Sel.Name) {
					return true
				}
				found = append(found, mention{
					in:     in,
					inLoop: within(loops, sel.Pos()),
					admits: given[sel],
					at:     fset.Position(sel.Pos()).String(),
				})
				return true
			})
		}
	}
	return found
}

// admitted reports whether call is given admit(x.needs, x.handle), for one
// x, as what it registers.
func admitted(call *ast.CallExpr) bool {
	if len(call.Args) != 2 {
		return false
	}
	wrap, ok := call.Args[1].(*ast.CallExpr)
	if !ok || len(wrap.Args) != 2 {
		return false
	}
	fun, ok := wrap.Fun.(*ast.SelectorExpr)
	if !ok || fun.Sel.Name != "admit" {
		return false
	}
	needsOf, needs := dotted(wrap.Args[0])
	handleOf, handle := dotted(wrap.Args[1])
	return needs == "needs" && handle == "handle" && needsOf != "" && needsOf == handleOf
}

// dotted reads x.name as its two names, and anything else as none.
func dotted(e ast.Expr) (of, name string) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	return x.Name, sel.Sel.Name
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
