package invisible_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/invisible"
)

func TestHas_FindsWhatARenderWouldNotShow(t *testing.T) {
	t.Parallel()
	// Written as escapes because none of them can be read in a source file,
	// which is the property that put them on the list.
	for _, c := range []struct{ name, text string }{
		{"a newline", "a\nb"},
		{"a tab", "a\tb"},
		{"a carriage return", "a\rb"},
		{"an escape sequence", "a\x1b[31mb"},
		{"a delete", "a\x7fb"},
		{"a C1 control", "a\u0085b"},
		{"a non-breaking space", "a\u00a0b"},
		{"a zero-width space", "a\u200bb"},
		{"a left-to-right mark", "a\u200eb"},
		{"a line separator", "a\u2028b"},
		{"a paragraph separator", "a\u2029b"},
		{"a right-to-left override", "a\u202eb"},
		{"a word joiner", "a\u2060b"},
		{"a first strong isolate", "a\u2066b"},
		{"a byte order mark", "a\ufeffb"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !invisible.Has(c.text) {
				t.Errorf("Has(%q) = false", c.text)
			}
			if got := invisible.Quote(c.text); got == c.text {
				t.Errorf("Quote left it as it was: %q", got)
			}
		})
	}
}

func TestHas_LeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"", "listen.port", "Ada Lovelace", "order A-1",
		"\u65e5\u672c\u5186", "0x0000000000000000000000000000000000000000",
	} {
		t.Run(text, func(t *testing.T) {
			if invisible.Has(text) {
				t.Errorf("Has(%q) = true", text)
			}
			if got := invisible.Quote(text); got != text {
				t.Errorf("Quote(%q) = %q, want it unchanged", text, got)
			}
		})
	}
}

// The shell script cannot import this package, so this reads its pattern and
// checks the two agree rather than leaving them to drift the way the three
// copies of this list did before it existed.
func TestScript_RefusesNothingThisPackageAllows(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile("../../scripts/check-source.sh")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^invisible='\[(.*)\]'$`).FindSubmatch(script)
	if line == nil {
		t.Fatal("scripts/check-source.sh has no invisible='[...]' line; this test cannot read it")
	}

	points := regexp.MustCompile(`\\x\{([0-9a-fA-F]+)\}`).FindAllSubmatch(line[1], -1)
	if len(points) == 0 {
		t.Fatal("the pattern names no code points")
	}
	for _, p := range points {
		n, err := strconv.ParseInt(string(p[1]), 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		if r := rune(n); !invisible.Has(string(r)) {
			t.Errorf("the script refuses %U, which this package allows", r)
		}
	}

	// The two differ in one direction on purpose: a source file is made of
	// tabs and newlines, so the script takes those back out.
	for _, allowed := range []struct {
		name string
		r    rune
	}{{"tab", '\t'}, {"newline", '\n'}} {
		if strings.Contains(string(line[1]), "\\x{"+strconv.FormatInt(int64(allowed.r), 16)+"}") {
			t.Errorf("the script names the %s that every source file holds", allowed.name)
		}
	}
}
