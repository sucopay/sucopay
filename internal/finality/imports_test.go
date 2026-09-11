package finality_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// allowed is everything a file of this package may import. An allow list
// rather than a block list, because the import worth catching is the one
// nobody thought to forbid: this package asks a chain through the boundary and
// writes what the answers settle through the payment store. A database driver
// reaching it would mean the deciding and the SQL had been put in one place.
var allowed = []string{
	"context", "errors", "fmt", "log/slog", "time",
	"github.com/sucopay/sucopay/internal/adapter/chain",
	"github.com/sucopay/sucopay/internal/payment",
}

func TestImports_ReachTheBoundaryAndNothingElse(t *testing.T) {
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
			for _, imported := range file.Imports {
				path, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if !slices.Contains(allowed, path) {
					t.Errorf("%s imports %s, which this package does not reach", name, path)
				}
			}
		})
	}
	if sources == 0 {
		t.Fatal("no source files found beside this one")
	}
}
