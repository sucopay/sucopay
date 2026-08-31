package payment_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// domainFiles hold the part of this package that a Payment is made of. The
// files around them reach outward on purpose: postgres.go holds a driver and
// http.go holds a server, and neither belongs on the list below.
var domainFiles = []string{"asset.go", "money.go", "payment.go", "status.go"}

// allowed is everything those files may import. An allow list rather than a
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
	seen := 0
	for _, name := range domainFiles {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		seen++
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
	if seen != len(domainFiles) {
		t.Errorf("checked %d files, want %d; the list in this test is stale", seen, len(domainFiles))
	}
}
