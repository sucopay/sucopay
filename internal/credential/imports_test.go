package credential_test

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
// what a credential is made of, and its place in a context.
// postgres.go holds a driver and reaches outward on purpose, and does not
// belong on the list below.
var domainFiles = []string{"credential.go", "context.go"}

// allowed is everything those files may import. An allow list rather than a
// block list, because the import worth catching is the one nobody thought to
// forbid: a driver, an HTTP handler, or whatever the next dependency is
// called. A credential shaped by what a database returns is no longer a
// credential.
var allowed = []string{
	"context", "crypto/hmac", "crypto/rand", "crypto/sha256", "encoding/hex",
	"errors", "fmt", "io", "log/slog", "regexp", "time",
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
	if seen != len(domainFiles) {
		t.Errorf("checked %d files, want %d; the list in this test is stale", seen, len(domainFiles))
	}
}
