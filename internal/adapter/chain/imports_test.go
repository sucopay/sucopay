package chain_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The boundary is what keeps a chain out of the observer, so nothing a chain's
// library or a driver would bring may come in through it. The standard library
// and no more: an import path whose first element has a dot in it names
// somebody else's module, or another package of this one.
func TestImports_ReachOnlyTheStandardLibrary(t *testing.T) {
	t.Parallel()
	for _, name := range sourceFiles(t) {
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
					t.Errorf("imports %q, which is not in the standard library", path)
				}
			}
		})
	}
}

// sourceFiles are the files of the package under test, less its tests. It
// fails rather than returning none, so that a test over them cannot pass by
// having looked at nothing.
func sourceFiles(t *testing.T) []string {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, name := range names {
		if !strings.HasSuffix(name, "_test.go") {
			sources = append(sources, name)
		}
	}
	if len(sources) == 0 {
		t.Fatal("found no source file to check")
	}
	return sources
}
