package observe_test

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
// nobody thought to forbid: this package reads a chain through the boundary
// and writes rows, and a chain's own library reaching it would put the shape
// of one chain into what every network is read with.
var allowed = []string{
	"context", "crypto/rand", "encoding/hex", "errors", "fmt", "math", "time",
	"github.com/jackc/pgx/v5", "github.com/jackc/pgx/v5/pgxpool",
	"github.com/sucopay/sucopay/internal/payment",
}

func TestImports_ReachTheDomainAndTheDriverAndNothingElse(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources++
		t.Run(name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range file.Imports {
				path := strings.Trim(spec.Path.Value, `"`)
				if !slices.Contains(allowed, path) {
					t.Errorf("imports %q, which is not on the allow list in this test", path)
				}
			}
		})
	}
	if sources == 0 {
		t.Fatal("found no source file to check")
	}
}
