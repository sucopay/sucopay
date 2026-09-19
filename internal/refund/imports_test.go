package refund_test

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
// what a token is and how one is derived, what a page shows, and what a
// merchant signs. http.go holds a handler and postgres.go a driver, and
// reach outward on purpose.
var domainFiles = []string{"token.go", "state.go", "typed.go"}

// allowed is everything those files may import. An allow list rather than a
// block list, because the import worth catching is the one nobody thought to
// forbid: a driver, an HTTP handler, or whatever the next dependency is
// called.
var allowed = []string{
	"crypto/hmac", "crypto/sha256", "encoding/hex", "errors", "fmt",
	"io", "log/slog", "strconv", "strings", "time",
	// payment is where a payment, its identifier and its refund are defined;
	// a token is of a refund and what is signed sends a payment back.
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
			for _, imported := range file.Imports {
				path := strings.Trim(imported.Path.Value, `"`)
				if !slices.Contains(allowed, path) {
					t.Errorf("imports %q, which is not on the allow list in this test", path)
				}
			}
		})
	}
	if seen != len(domainFiles) {
		t.Errorf("%d of the %d files named were read", seen, len(domainFiles))
	}
}
