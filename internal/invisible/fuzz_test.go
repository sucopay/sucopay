package invisible_test

import (
	"strings"
	"testing"

	"github.com/sucopay/sucopay/internal/invisible"
)

// Everything this project shows a person goes through Quote, so the one thing
// it must never do is hand back something a terminal would act on.
func FuzzQuote(f *testing.F) {
	// One seed per character the list claims to refuse, and one either side of
	// each range. Without them the seed corpus alone would not notice a
	// character dropped from the list, however good the check is.
	for _, seed := range []string{
		"", "listen.port", "a b", "\u3042",
		"a\u0000b",
		"a\u0009b",
		"a\u000ab",
		"a\u001fb",
		"a\u007fb",
		"a\u0085b",
		"a\u009fb",
		"a\u00a0b",
		"a\u200bb",
		"a\u200fb",
		"a\u2028b",
		"a\u2029b",
		"a\u202ab",
		"a\u202eb",
		"a\u2060b",
		"a\u2066b",
		"a\u2069b",
		"a\ufeffb",
		strings.Repeat("a", 1000),
	} {
		f.Add(seed)
	}

	// Asking Has whether Quote did its job routes both through one decision,
	// so a character dropped from that decision is missed by both at once.
	// What Quote may hand back is written out here instead.
	unreadable := func(r rune) bool {
		switch {
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return true
		case r == 0x00a0, r == 0x2060, r == 0xfeff:
			return true
		case r >= 0x200b && r <= 0x200f:
			return true
		case r >= 0x2028 && r <= 0x202e:
			return true
		case r >= 0x2066 && r <= 0x2069:
			return true
		}
		return false
	}

	f.Fuzz(func(t *testing.T, s string) {
		got := invisible.Quote(s)

		for _, r := range got {
			if unreadable(r) {
				t.Errorf("Quote(%q) = %q, which still holds %U", s, got, r)
			}
		}
		if !strings.ContainsFunc(s, unreadable) && got != s {
			t.Errorf("Quote changed %q to %q, which held nothing to quote", s, got)
		}
	})
}
