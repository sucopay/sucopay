package config_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImports_TheResolutionFilesTouchNothingOutsideThemselves keeps the
// dependency rule from CONTRIBUTING.md enforceable rather than reviewable.
// Resolution has to stay callable without a filesystem, an environment or a
// parser, so that its tests can cover every case as data.
func TestImports_TheResolutionFilesTouchNothingOutsideThemselves(t *testing.T) {
	inner := map[string]bool{
		"config.go":  true,
		"resolve.go": true,
		"report.go":  true,
	}
	forbidden := []string{
		"os",
		"io",
		"io/fs",
		"path/filepath",
		"net/http",
		"database/sql",
		"github.com/goccy/go-yaml",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !inner[name] {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			for _, path := range importsOf(t, name) {
				for _, bad := range forbidden {
					if path == bad || strings.HasPrefix(path, bad+"/") {
						t.Errorf("imports %q", path)
					}
				}
			}
		})
	}
	if seen != len(inner) {
		t.Errorf("checked %d files, want %d; the list in this test is stale", seen, len(inner))
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
