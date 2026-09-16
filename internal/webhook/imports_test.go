package webhook_test

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
// what an endpoint is, what a destination may be, and what a secret is.
// postgres.go holds a driver and http.go a handler, and reach outward on
// purpose.
var domainFiles = []string{"endpoint.go", "destination.go", "secret.go"}

// allowed is everything those files may import. An allow list rather than a
// block list, because the import worth catching is the one nobody thought to
// forbid: a driver, an HTTP handler, or whatever the next dependency is
// called.
var allowed = []string{
	"context", "crypto/aes", "crypto/cipher", "crypto/rand", "encoding/base64",
	"errors", "fmt", "io", "log/slog", "net", "net/netip", "net/url", "slices",
	"strings", "time",
	// invisible decides which characters a URL and a description may carry.
	// It reads nothing and reaches nothing; it is a list of runes.
	"github.com/sucopay/sucopay/internal/invisible",
	// payment is where an account's identifier and the shape of an id are
	// defined; an endpoint is of an account, and a delivery is of a payment.
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
