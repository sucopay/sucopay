package checkout_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// domainFiles hold the part of this package that reaches nothing outward:
// what a token is and how one is derived. A file that holds a handler or a
// driver reaches outward on purpose and does not belong on this list.
var domainFiles = []string{"token.go"}

// allowed is everything those files may import. An allow list rather than a
// block list, because the import worth catching is the one nobody thought to
// forbid: a driver, an HTTP handler, or whatever the next dependency is
// called.
var allowed = []string{
	"crypto/hmac", "crypto/sha256", "encoding/hex", "encoding/json", "errors", "fmt",
	"io", "log/slog", "math/big", "strconv", "strings", "time",
	// payment is where a payment, its id and its attempt are defined; a
	// token is of a payment and what is signed is against an attempt.
	"github.com/sucopay/sucopay/internal/payment",
}

func TestImports_TheDomainReachesNothingOutsideItself(t *testing.T) {
	t.Parallel()
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
	if seen == 0 {
		t.Fatal("none of the domain files exist, so nothing was checked")
	}
}
