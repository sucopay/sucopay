package accepted_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// allowed is everything a file of this package may import. An allow list
// rather than a block list, because the import worth catching is the one
// nobody thought to forbid: a handler, a configuration document, or whatever
// the next dependency is called. This package stores what the domain says an
// account accepts; it reads nothing about how a request or a document says
// it.
var allowed = []string{
	"context", "errors", "fmt", "time",
	"github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgxpool",
	"github.com/sucopay/sucopay/internal/payment",
}

func TestImports_ReachesTheDomainAndTheDriverAndNothingElse(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				if path := strings.Trim(spec.Path.Value, `"`); !slices.Contains(allowed, path) {
					t.Errorf("imports %q, which is not on the allow list in this test", path)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("no files to check, so this test is checking nothing")
	}
}
