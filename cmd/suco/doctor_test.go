package main

import (
	"strings"
	"testing"
)

func TestDescribeVersion_BoundsAndQuotesWhatTheServerSent(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	if got := describeVersion("17.11"); got != "PostgreSQL 17.11" {
		t.Errorf("describeVersion(17.11) = %q, want it unchanged", got)
	}
}

// A document is read into the form the chain compares, so that a reference
// written the way a block explorer shows one is the same asset as the one a
// transfer names.
func TestRun_DoctorShowsAReferenceTheWayTheChainWritesIt(t *testing.T) {
	document(t, anEVMAssetOn("local"))

	stdout, _, err := runArgs(t, "doctor")

	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	line := reportLine(t, stdout, "assets.jpyc.reference")
	if !strings.Contains(line, strings.ToLower(theChecksummed)) {
		t.Errorf("assets.jpyc.reference reads %q, want it in lower case", line)
	}
}
