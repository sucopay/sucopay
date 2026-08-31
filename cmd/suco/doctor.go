package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/sucopay/sucopay/internal/invisible"
	"github.com/sucopay/sucopay/internal/postgres"
)

// maxDescription bounds what a database can put in front of an operator.
// Nothing limits the length of what a server sends.
const maxDescription = 64

func doctor(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) > 0 {
		return fmt.Errorf("doctor takes no arguments, got %q", args[0])
	}

	resolved, document, err := load()
	if err != nil {
		return err
	}

	// What this command produces is the bytes, and half of them is worse than
	// none: a full disk would otherwise leave a truncated report behind a
	// status saying it went well.
	var report bytes.Buffer
	fmt.Fprintf(&report, "%s\n\n", document)
	tw := tabwriter.NewWriter(&report, 0, 0, 2, ' ', 0)
	for _, line := range resolved.Report() {
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", line.Path, line.Value, line.Source)
	}
	tw.Flush()

	// A server that answers slowly must not take away the part of the report
	// that needed nothing from it.
	if _, err := stdout.Write(report.Bytes()); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}

	database, reach := describeDatabase(ctx, resolved.Config.Database.URL)
	if _, err := fmt.Fprintf(stdout, "\ndatabase: %s\n\n", database); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}

	// A report that showed a settled configuration and left these out would be
	// read as saying the instance will start.
	if err := unimplementedError(document, resolved); err != nil {
		return err
	}
	return reach
}

// describeDatabase reports what an instance would reach, and separately why it
// would not. The description is written into the report either way, so that a
// failure is shown beside the setting that caused it rather than on its own.
//
// Everything here was chosen by a server at the other end of a network, so it
// is bounded and quoted before it reaches a terminal.
func describeDatabase(ctx context.Context, url string) (string, error) {
	if url == "" {
		return "none configured", nil
	}
	db, err := postgres.Open(ctx, url)
	if err != nil {
		return "unreachable", errors.New(invisible.Quote(err.Error()))
	}
	defer db.Close()

	version, err := db.ServerVersion(ctx)
	if err != nil {
		return "unreachable", errors.New(invisible.Quote(err.Error()))
	}
	return describeVersion(version), nil
}

// describeVersion renders what a server said about itself for a report a
// person reads.
func describeVersion(version string) string {
	return "PostgreSQL " + invisible.Quote(shorten(version, maxDescription))
}

// shorten cuts s to at most n bytes, on a rune boundary.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
