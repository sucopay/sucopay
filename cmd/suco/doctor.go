package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"text/tabwriter"
)

func doctor(_ context.Context, args []string, stdout io.Writer) error {
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
	fmt.Fprintln(&report)

	if _, err := stdout.Write(report.Bytes()); err != nil {
		return fmt.Errorf("writing the report: %w", err)
	}

	// A report that showed a settled configuration and left this out would be
	// read as saying the instance will start.
	return unimplementedError(document, unimplemented(resolved))
}
