package postgres_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// This package is the outermost thing in the process: it holds a driver and a
// socket. Nothing of suco Pay may travel back through it, or the dependency
// rule is inverted and a domain type ends up shaped by what a driver returns.
func TestImports_ReachesNothingElseInThisProject(t *testing.T) {
	const module = "github.com/sucopay/sucopay/"

	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no files to check, so this test is checking nothing")
	}

	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				if path := strings.Trim(spec.Path.Value, `"`); strings.HasPrefix(path, module) {
					t.Errorf("imports %q, which is a package this one is underneath", path)
				}
			}
		})
	}
}
