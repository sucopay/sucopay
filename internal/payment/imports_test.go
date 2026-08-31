package payment_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// allowed is everything the domain may import. An allow list rather than a
// block list, because the import worth catching is the one nobody thought to
// forbid: a driver, an HTTP handler, or whatever the next dependency is
// called. A Payment shaped by what a database returns is no longer a Payment.
var allowed = []string{
	"crypto/rand", "encoding/hex", "errors", "fmt", "maps",
	"math/big", "slices", "strings", "time",
	// invisible decides which characters metadata may carry. It reads
	// nothing and reaches nothing; it is a list of runes.
	"github.com/sucopay/sucopay/internal/invisible",
}

func TestImports_TheDomainReachesNothingOutsideItself(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no files to check, so this test is checking nothing")
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
				path := strings.Trim(spec.Path.Value, `"`)
				if !slices.Contains(allowed, path) {
					t.Errorf("imports %q, which is not on the allow list in this test", path)
				}
			}
		})
	}
	if checked == 0 {
		t.Fatal("every file was skipped, so this test is checking nothing")
	}
}
