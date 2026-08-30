package config_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

// TestDocument_ResolvesWithoutAProblem is the invariant that ties `suco init`
// to `suco serve`. An instance that refuses to start without a document is only
// usable if the document it is handed is one it accepts.
func TestDocument_ResolvesWithoutAProblem(t *testing.T) {
	doc, err := config.Decode(config.Document())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	got, err := config.Resolve(doc, noEnv)

	if err != nil {
		t.Fatalf("the document suco init writes does not resolve: %v", err)
	}
	if got.Config.Listen.Host != config.DefaultHost {
		t.Errorf("host = %q, want %q", got.Config.Listen.Host, config.DefaultHost)
	}
	if got.Config.Listen.Port != config.DefaultPort {
		t.Errorf("port = %d, want %d", got.Config.Listen.Port, config.DefaultPort)
	}
}

// TestDocument_WritesEveryValueItChose keeps the document a record rather than
// a set of overrides. A key left out would put the decision back inside the
// binary where nobody running it can see it.
func TestDocument_WritesEveryValueItChose(t *testing.T) {
	doc, err := config.Decode(config.Document())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	resolved, err := config.Resolve(doc, noEnv)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	for path, source := range resolved.Sources {
		if !strings.HasPrefix(path, "listen.") {
			continue
		}
		if source.Origin != config.FromFile {
			t.Errorf("%s came from %v, want the document to carry it", path, source.Origin)
		}
	}
}

// TestDocument_NamesNoSubsystemThatDoesNotExist stops the document from
// describing a server that ignores what it was told.
func TestDocument_NamesNoSubsystemThatDoesNotExist(t *testing.T) {
	doc, err := config.Decode(config.Document())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, absent := range []string{"database", "networks"} {
		if _, ok := doc[absent]; ok {
			t.Errorf("the document names %q, which nothing acts on yet", absent)
		}
	}
}
