package payment_test

import (
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// domainFiles hold the part of this package that a Payment is made of, and the
// contract for storing one. The files around them reach outward on purpose:
// postgres.go holds a driver and http.go holds a server, and neither belongs on
// the list below. repository.go does: a driver type reaching the interface is
// how the shape of a database gets into everything that stores a payment.
var domainFiles = []string{
	"asset.go", "attempt.go", "evidence.go", "money.go", "payment.go",
	"repository.go", "service.go", "status.go",
}

// allowed is everything those files may import. An allow list rather than a
// block list, because the import worth catching is the one nobody thought to
// forbid: a driver, an HTTP handler, or whatever the next dependency is
// called. A Payment shaped by what a database returns is no longer a Payment.
var allowed = []string{
	"bytes", "context", "crypto/rand", "encoding/hex", "errors", "fmt", "maps",
	"math/big", "slices", "strings", "time",
	// invisible decides which characters metadata may carry. It reads
	// nothing and reaches nothing; it is a list of runes.
	"github.com/sucopay/sucopay/internal/invisible",
	// problem renders a list of them for a reader. It reads nothing and
	// reaches nothing; it is a way of laying out strings.
	"github.com/sucopay/sucopay/internal/problem",
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

// The rules read what a chain said, and a chain says it in its own words. A
// word of one chain in the code here would be a rule that only holds on that
// chain, and the next adapter would have to be written around it.
//
// The code, and not the comments: one of them names the chain shapes this
// package refuses to know, which is the rule holding rather than breaking.
func TestSource_HoldsNoWordOfAKindOfChain(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		read++
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, filepath.Clean(name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			var code strings.Builder
			if err := printer.Fprint(&code, fset, file); err != nil {
				t.Fatal(err)
			}
			text := strings.ToLower(code.String())
			for _, word := range []string{"evm", "simulated"} {
				if strings.Contains(text, word) {
					t.Errorf("holds %q, which is the name of one kind of chain", word)
				}
			}
		})
	}
	if read == 0 {
		t.Fatal("found no source file to read")
	}
}
