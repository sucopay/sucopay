package config_test

import (
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

// testKeyID is a key identifier as init makes one: eight bytes as
// hexadecimal.
const testKeyID = "0123456789abcdef"

// keyEnv is the environment the line init prints leaves behind: the key,
// thirty-two bytes as hexadecimal, under the variable the document names.
var keyEnv = envOf(map[string]string{
	config.KeyVar: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
})

// TestDocument_ResolvesWithoutAProblem is the invariant that ties `suco init`
// to `suco serve`. An instance that refuses to start without a document is only
// usable if the document it is handed is one it accepts, once the line init
// prints has been run.
func TestDocument_ResolvesWithoutAProblem(t *testing.T) {
	t.Parallel()
	doc, err := config.Decode(config.Document(testKeyID))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	got, err := config.Resolve(doc, keyEnv)

	if err != nil {
		t.Fatalf("the document suco init writes does not resolve: %v", err)
	}
	if got.Config.Listen.Host != config.DefaultHost {
		t.Errorf("host = %q, want %q", got.Config.Listen.Host, config.DefaultHost)
	}
	if got.Config.Listen.Port != config.DefaultPort {
		t.Errorf("port = %d, want %d", got.Config.Listen.Port, config.DefaultPort)
	}
	if got.Config.Credentials.KeyID != testKeyID {
		t.Errorf("key_id = %q, want %q", got.Config.Credentials.KeyID, testKeyID)
	}
}

// TestDocument_NamesTheVariableTheKeyComesFrom is the half of init that a
// document can hold: the key is read from the environment, and the document
// says from where.
func TestDocument_NamesTheVariableTheKeyComesFrom(t *testing.T) {
	t.Parallel()
	doc, err := config.Decode(config.Document(testKeyID))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	resolved, err := config.Resolve(doc, keyEnv)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	want := config.Source{Origin: config.FromEnv, Var: config.KeyVar}
	if got := resolved.Sources["credentials.key"]; got != want {
		t.Errorf("credentials.key came from %+v, want %+v", got, want)
	}
}

// TestDocument_KeepsAKeyIDOfDigitsText guards the one value in the document
// that is not chosen by hand. Sixteen hexadecimal digits are sometimes all
// decimal ones, and now and then digits around one e, and YAML reads either
// as a number unless the document says otherwise.
func TestDocument_KeepsAKeyIDOfDigitsText(t *testing.T) {
	t.Parallel()
	for _, keyID := range []string{"1234567890123456", "12e4567890123456"} {
		t.Run(keyID, func(t *testing.T) {
			t.Parallel()
			doc, err := config.Decode(config.Document(keyID))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}

			resolved, err := config.Resolve(doc, keyEnv)

			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if resolved.Config.Credentials.KeyID != keyID {
				t.Errorf("key_id = %q, want %q", resolved.Config.Credentials.KeyID, keyID)
			}
		})
	}
}

// TestDocument_CarriesEveryValueItNames keeps the document a record. A value
// left to a default would put the decision back inside the binary where nobody
// running the instance can see it. A secret is the exception the document
// makes itself: it names the variable, and the value is the environment's.
func TestDocument_CarriesEveryValueItNames(t *testing.T) {
	t.Parallel()
	doc, err := config.Decode(config.Document(testKeyID))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	resolved, err := config.Resolve(doc, keyEnv)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	for _, path := range leafPaths(t, doc, "") {
		source := resolved.Sources[path]
		switch {
		case source.Origin == config.FromDefault:
			t.Errorf("%s came from %v, want the document to carry it", path, source.Origin)
		case source.Origin == config.FromEnv && !config.Secret(path):
			t.Errorf("%s came from %v, which only a secret is left to", path, source.Origin)
		}
	}
}

// leafPaths lists the dotted path of every value in a decoded document.
func leafPaths(t *testing.T, doc map[string]any, prefix string) []string {
	t.Helper()
	var out []string
	for key, value := range doc {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if nested, ok := value.(map[string]any); ok && len(nested) > 0 {
			out = append(out, leafPaths(t, nested, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// TestDocument_NamesNoSubsystemThatDoesNotExist stops the document from
// describing a server that ignores what it was told.
func TestDocument_NamesNoSubsystemThatDoesNotExist(t *testing.T) {
	t.Parallel()
	doc, err := config.Decode(config.Document(testKeyID))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, absent := range []string{"database", "networks"} {
		if _, ok := doc[absent]; ok {
			t.Errorf("the document names %q, which nothing acts on yet", absent)
		}
	}
}
