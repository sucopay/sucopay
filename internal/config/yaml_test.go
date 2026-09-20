package config_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/config"
)

// TestDecode_KeepsTheSourceOutOfASyntaxError is the regression that matters
// most in this package. The parser can render the lines around a mistake, and
// those lines hold whatever the document holds. An unclosed quote below a
// database URL is an ordinary typing mistake, not an attack.
func TestDecode_KeepsTheSourceOutOfASyntaxError(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-do-not-print"
	cases := []struct {
		name string
		doc  string
	}{
		{
			name: "a quote left open below a secret",
			doc:  "database:\n  url: postgres://admin:" + secret + "@db/pay\nlisten:\n  host: \"unterminated\n",
		},
		{
			name: "a key repeated below a secret",
			doc:  "database:\n  url: postgres://admin:" + secret + "@db/pay\n  url: other\n",
		},
		{
			name: "a tab where indentation belongs",
			doc:  "database:\n  url: postgres://admin:" + secret + "@db/pay\n\tbroken: 1\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Decode([]byte(c.doc))

			if err == nil {
				t.Fatal("want an error, got none")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("the document reached the message:\n%v", err)
			}
		})
	}
}

// TestDecode_RefusesAnchorsAndAliases covers the amplification an alias inside
// a mapping allows: each level names a copy of the one below it, and a document
// of a few hundred bytes exhausts memory before any limit on its size applies.
func TestDecode_RefusesAnchorsAndAliases(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		doc  string
	}{
		{"an anchor", "a: &x 1\nb: 2\n"},
		{"an alias", "a: &x 1\nb: *x\n"},
		{"an alias inside a mapping", "a0: &a0 {k: v}\na1: {k0: *a0, k1: *a0}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := config.Decode([]byte(c.doc))

			if !errors.Is(err, config.ErrDocumentHasAliases) {
				t.Fatalf("err = %v, want ErrDocumentHasAliases", err)
			}
		})
	}
}

func TestDecode_RefusesADocumentPastTheSizeLimit(t *testing.T) {
	t.Parallel()
	doc := []byte("a: " + strings.Repeat("x", config.MaxDocumentBytes))

	_, err := config.Decode(doc)

	if !errors.Is(err, config.ErrDocumentTooLarge) {
		t.Fatalf("err = %v, want ErrDocumentTooLarge", err)
	}
	// The one refusal the fuzzer never reaches: 256 KiB is past what it
	// writes, so its list of Decode's messages is held to this one here.
	if !said.MatchString(err.Error()) {
		t.Errorf("err = %q, which FuzzDecode's said does not know", err)
	}
}

func TestDecode_AcceptsADocumentAtTheSizeLimit(t *testing.T) {
	t.Parallel()
	doc := []byte("a: " + strings.Repeat("x", config.MaxDocumentBytes-3))

	if _, err := config.Decode(doc); err != nil {
		t.Fatalf("a document of exactly the limit was refused: %v", err)
	}
}
