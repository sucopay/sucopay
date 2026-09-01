// Package problem renders what is wrong with something a person supplied.
//
// A configuration document and a payment request are different things and
// their problems name different parts of them, so each package keeps its own
// type. What they share is how a list of them is put in front of a reader,
// which had come to be written twice.
package problem

import (
	"fmt"
	"strings"

	"github.com/sucopay/sucopay/internal/invisible"
)

// Line renders one problem. The key is quoted: it comes from whatever was
// supplied, so an unquoted one could carry a newline and forge a line of its
// own, or an escape sequence a terminal would act on.
func Line(key, message string) string {
	if key == "" {
		return message
	}
	return invisible.Quote(key) + ": " + message
}

// List renders every problem under one subject, so that a caller learns
// everything wrong with what they sent rather than one thing per attempt.
func List(subject string, lines []string) string {
	if len(lines) == 1 {
		return subject + ": " + lines[0]
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, fmt.Sprintf("%s: %d problems", subject, len(lines)))
	for _, line := range lines {
		out = append(out, "  "+line)
	}
	return strings.Join(out, "\n")
}
