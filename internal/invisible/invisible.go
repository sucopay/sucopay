// Package invisible names the characters a reader cannot see, in one place.
//
// A terminal acts on some of these rather than showing them: a newline forges
// a line of its own in a report laid out in columns, an escape sequence is
// obeyed, and a bidirectional override reverses the reading order of what
// follows. None of them leave a byte anyone would notice.
//
// Three places refuse them for three reasons: a configuration document may not
// carry them into a report, payment metadata may not carry them into a console
// or a webhook, and a source file may not carry them past a reviewer. One list
// keeps the three from disagreeing about which characters those are.
package invisible

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Has reports whether s holds a character that does not show up. An ordinary
// space is not one of them; a non-breaking space is, because it reads as a
// space and is not one.
func Has(s string) bool {
	return strings.IndexFunc(s, is) >= 0
}

// Quote renders s readably, quoting it only when [Has] would refuse it. It is
// for text that has to be shown rather than refused, such as the key a
// configuration document chose to hold a mistake.
func Quote(s string) string {
	if !Has(s) {
		return s
	}
	return strconv.Quote(s)
}

// Shown renders text from somewhere else so that it can be put in front of a
// person: cut to at most max bytes on a rune boundary, then quoted.
//
// Both halves are needed. Nothing bounds what a request path or a server's
// reply can be, and slog's JSON handler passes a zero-width space or a
// right-to-left override through as the bytes it was given: only its text
// handler escapes them, and JSON is what a deployment writes.
func Shown(s string, max int) string {
	// A bound below nothing is nothing. Cutting to it would run off the front
	// of the string rather than shorten it, which is a caller's arithmetic
	// ending the process rather than shortening a line.
	if max < 0 {
		max = 0
	}
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return Quote(s)
}

// is reports whether r is a character a reader cannot see. Written as escapes
// for the reason the list exists.
func is(r rune) bool {
	switch {
	case r < 0x20, r == 0x7f:
		// The C0 controls and delete. Tab and newline are here too: a source
		// file is made of them, and scripts/check-source.sh takes them back
		// out for that reason, but nothing else may carry one.
		return true
	case r >= 0x80 && r <= 0x9f:
		return true
	case r == '\u00a0':
		return true
	case r >= '\u200b' && r <= '\u200f':
		// The zero-width space, joiners and marks. Two keys differing by one
		// of these are different keys that render identically.
		return true
	case r == '\u2028', r == '\u2029':
		return true
	case r >= '\u202a' && r <= '\u202e':
		return true
	case r == '\u2060':
		return true
	case r >= '\u2066' && r <= '\u2069':
		return true
	case r == '\ufeff':
		return true
	}
	return false
}
