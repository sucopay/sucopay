package main

import (
	"strings"
	"testing"
)

func TestDescribeVersion_BoundsAndQuotesWhatTheServerSent(t *testing.T) {
	// The report is laid out in columns and reaches a terminal. What arrives
	// in this field was chosen at the other end of a network, by whoever is
	// answering, which is not always the database the operator meant.
	for _, c := range []struct {
		name    string
		version string
	}{
		{"a row of its own", "17.11\n  database.url\tnot set\tdefault"},
		{"an escape sequence", "17.11\x1b[31m all clear\x1b[0m"},
		{"a column separator", "17.11\tsomething else"},
		{"more than a line", strings.Repeat("17.11 ", 200)},
		{"multi-byte, cut mid-rune", strings.Repeat("バージョン", 100)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := describeVersion(c.version)

			if strings.ContainsAny(got, "\n\t\x1b") {
				t.Errorf("reaches the report unquoted: %q", got)
			}
			if len(got) > maxDescription*4 {
				t.Errorf("length %d, want something a report can hold: %q", len(got), got)
			}
		})
	}
}

func TestDescribeVersion_LeavesAnOrdinaryVersionAlone(t *testing.T) {
	if got := describeVersion("17.11"); got != "PostgreSQL 17.11" {
		t.Errorf("describeVersion(17.11) = %q, want it unchanged", got)
	}
}
