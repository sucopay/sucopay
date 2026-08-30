package config_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// resolutionFiles hold the part of this package that turns a document into a
// [config.Config]. They stay callable without a filesystem, an environment or a
// parser, so that their tests can cover every case as data.
var resolutionFiles = []string{"config.go", "report.go", "resolve.go"}

// resolutionImports is everything those files may import. An allow list rather
// than a block list, because the imports worth catching are the ones nobody
// thought to forbid: an outward package of this project, or whatever a future
// dependency is called.
var resolutionImports = []string{
	"cmp", "errors", "fmt", "math", "net/url", "slices", "strconv", "strings",
}

func TestImports_ResolutionReachesNothingOutsideItself(t *testing.T) {
	seen := 0
	for _, name := range resolutionFiles {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			for _, path := range importsOf(t, name) {
				if !slices.Contains(resolutionImports, path) {
					t.Errorf("imports %q, which is not on the allow list in this test", path)
				}
			}
		})
	}
	if seen != len(resolutionFiles) {
		t.Errorf("checked %d files, want %d; the list in this test is stale", seen, len(resolutionFiles))
	}
}

func importsOf(t *testing.T, name string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(file.Imports))
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}
