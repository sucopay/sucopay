package evm

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// An adapter is written against the boundary and against nothing else of this
// project. Reaching into a context from here would put a chain's own words
// into the domain, and the words this chain uses for a scheme and for a status
// are its own even where they read the same.
func TestImports_ReachTheBoundaryAndNoOtherPartOfThisProject(t *testing.T) {
	t.Parallel()
	const module = "github.com/sucopay/sucopay/"
	allowed := []string{
		module + "internal/adapter/chain",
		// What a provider wrote is repeated in errors, and a character that
		// does not show up is not repeated with it.
		module + "internal/invisible",
	}

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
				path, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(path, module) && !slices.Contains(allowed, path) {
					t.Errorf("imports %q, which is not the boundary", path)
				}
			}
		})
	}
}
